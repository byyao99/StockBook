// Package db is the persistence layer. It wraps GORM so no other package needs
// to know about it, and translates driver errors into the sentinels that
// handlers map to HTTP status codes.
package db

import (
	"errors"
	"log/slog"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"stockbook/internal/models"
)

// ErrNotFound is returned when a requested resource does not exist.
var ErrNotFound = errors.New("resource not found")

// ErrConflict is returned when a write lost a race: the row no longer holds the
// values the caller last observed, so applying the write would silently
// overwrite someone else's change. Handlers map it to HTTP 409.
var ErrConflict = errors.New("resource was modified concurrently")

// ErrSymbolTaken is returned by CreateInstrument when the symbol already exists.
var ErrSymbolTaken = errors.New("symbol already taken")

// ErrInstrumentInUse is returned when deleting an instrument that transactions
// still reference. Removing it would orphan those rows, and the symbol snapshot
// they carry is not enough to rebuild a position from.
var ErrInstrumentInUse = errors.New("instrument is referenced by existing transactions")

// ErrCurrencyLocked is returned when changing the currency of an instrument that
// already has trades against it. Every price and cost basis recorded under it
// was denominated in the old currency, so switching would reinterpret the whole
// history rather than correct it.
var ErrCurrencyLocked = errors.New("currency cannot be changed once trades exist")

// ListOptions controls pagination and sorting for list queries.
// Sort is a column name; it is validated against a per-resource allowlist
// before reaching SQL, so it is never a SQL-injection vector. Order is
// "asc" or "desc". Zero values fall back to each list method's defaults.
type ListOptions struct {
	Offset int
	Limit  int
	Sort   string
	Order  string
}

// orderClause builds a safe "<column> <direction>" ORDER BY fragment.
// opts.Sort is honored only if present in allowed; otherwise defaultSort is
// used. Direction is "asc"/"desc", defaulting to defaultOrder.
func orderClause(opts ListOptions, allowed map[string]bool, defaultSort, defaultOrder string) string {
	col := defaultSort
	if opts.Sort != "" && allowed[opts.Sort] {
		col = opts.Sort
	}
	dir := defaultOrder
	switch strings.ToLower(opts.Order) {
	case "asc":
		dir = "asc"
	case "desc":
		dir = "desc"
	}
	return col + " " + dir
}

// instrumentSortColumns are the columns clients may sort instruments by.
var instrumentSortColumns = map[string]bool{
	"symbol": true, "name": true, "market": true,
	"last_price": true, "created_at": true, "updated_at": true,
}

// transactionSortColumns are the columns clients may sort a ledger by.
var transactionSortColumns = map[string]bool{
	"traded_at": true, "symbol": true, "side": true, "quantity": true,
	"price": true, "net_amount": true, "created_at": true, "updated_at": true,
}

// userSortColumns are the columns admins may sort the user list by.
var userSortColumns = map[string]bool{
	"username": true, "role": true, "created_at": true, "updated_at": true,
}

// DB persists instruments, ledger transactions, positions and users in a SQLite
// database via GORM.
type DB struct {
	db *gorm.DB
}

// Open opens the SQLite database at dsn, runs migrations, and returns a DB.
// dsn is typically a file path such as "stockbook.db".
func Open(dsn string) (*DB, error) {
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Warn),
		TranslateError: true, // surface gorm.ErrDuplicatedKey etc. as typed errors
	})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(
		&models.User{},
		&models.Instrument{},
		&models.Transaction{},
		&models.Position{},
		&models.DailyClose{},
	); err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	// SQLite allows only one writer at a time, so a connection pool buys no
	// write concurrency — it only turns concurrent writes into SQLITE_BUSY
	// errors. A single connection serializes access at the pool instead.
	//
	// Note this makes the compare-and-swap in the position writes look
	// redundant here, but it is not: the CAS is what keeps those writes correct
	// on an engine that does allow concurrent writers, and the concurrency tests
	// exercise it as if we were on one.
	sqlDB.SetMaxOpenConns(1)

	d := &DB{db: db}
	if err := d.backfillCurrencies(); err != nil {
		return nil, err
	}
	if err := d.backfillRealizedPL(); err != nil {
		return nil, err
	}
	return d, nil
}

// backfillCurrencies gives a currency to instruments created before the column
// existed. AutoMigrate adds a column but leaves existing rows at the zero value,
// and an empty currency would silently drop those holdings out of every
// per-currency total. The market each instrument trades on is the best available
// guess, and an admin can correct it afterwards.
func (d *DB) backfillCurrencies() error {
	var stale []models.Instrument
	if err := d.db.Where("currency IS NULL OR currency = ''").Find(&stale).Error; err != nil {
		return err
	}
	for _, item := range stale {
		currency := models.DefaultCurrencyForMarket(item.Market)
		if err := d.db.Model(&models.Instrument{}).
			Where("id = ?", item.ID).Update("currency", currency).Error; err != nil {
			return err
		}
	}
	return nil
}

// backfillRealizedPL stamps realizing entries recorded before the column
// existed. They are replayed holding by holding, because what a sell realized
// depends on the entries before it and cannot be recovered from the row alone.
//
// Only holdings with an unstamped entry are touched, so this is a no-op on every
// start after the first, and it writes nothing to the positions table — those
// rows are already correct and this is a decomposition of them, not a
// correction.
//
// A holding whose ledger no longer replays (a book left inconsistent by some
// earlier bug) is logged and skipped rather than failing startup: its entries
// stay unstamped, which the report counts and shows rather than quietly reading
// as zero. Refusing to open the database over one bad holding would take the
// whole application down for a problem confined to one symbol.
func (d *DB) backfillRealizedPL() error {
	type holding struct {
		UserID       string
		InstrumentID string
	}
	var stale []holding
	if err := d.db.Model(&models.Transaction{}).
		Where("side <> ? AND realized_pl IS NULL", models.SideBuy).
		Distinct("user_id", "instrument_id").
		Scan(&stale).Error; err != nil {
		return err
	}

	for _, h := range stale {
		err := d.db.Transaction(func(tx *gorm.DB) error {
			_, entries, err := foldLedger(tx, h.UserID, h.InstrumentID)
			if err != nil {
				return err
			}
			return restampLedger(tx, entries)
		})
		if errors.Is(err, models.ErrInsufficientShares) {
			slog.Warn("skipped realized P&L backfill for an inconsistent holding",
				slog.String("user_id", h.UserID),
				slog.String("instrument_id", h.InstrumentID),
				slog.Any("error", err),
			)
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Close releases the underlying database connection.
func (d *DB) Close() error {
	db, err := d.db.DB()
	if err != nil {
		return err
	}
	return db.Close()
}

// Ping verifies the database connection is alive.
func (d *DB) Ping() error {
	db, err := d.db.DB()
	if err != nil {
		return err
	}
	return db.Ping()
}

// translate converts a GORM error into this package's sentinels.
func translate(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}
