// Package db is the persistence layer. It wraps GORM so no other package needs
// to know about it, and translates driver errors into the sentinels that
// handlers map to HTTP status codes.
package db

import (
	"errors"
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
	return &DB{db: db}, nil
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
