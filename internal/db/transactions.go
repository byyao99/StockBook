package db

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"stockbook/internal/models"
)

// ledgerOrder is the total order in which a user's transactions are folded into
// a position. It must be a total order, not just a chronological one: with only
// traded_at, several trades on the same day would fold in an arbitrary order and
// the moving-average cost would drift between replays. created_at breaks ties
// between same-day trades, and id breaks ties between rows created in the same
// clock tick.
const ledgerOrder = "traded_at asc, created_at asc, id asc"

// CreateTransaction appends a transaction to a user's ledger and brings the
// affected position back in line, both inside a single database transaction.
//
// The caller supplies the ID, UserID, InstrumentID, Side, Quantity, Price, Fee,
// TradedAt and Note. Symbol and NetAmount are filled in here from the
// instrument and the server's own arithmetic — a client-supplied amount is never
// trusted.
//
// It returns ErrNotFound if the instrument does not exist,
// models.ErrInsufficientShares if a sell would exceed the holding, and
// ErrConflict if a concurrent write changed the position underneath it.
func (d *DB) CreateTransaction(t models.Transaction) (models.Transaction, error) {
	err := d.db.Transaction(func(tx *gorm.DB) error {
		// Resolve the instrument inside the transaction so it cannot be deleted
		// between validation and the write.
		var instrument models.Instrument
		if err := tx.First(&instrument, "id = ?", t.InstrumentID).Error; err != nil {
			return translate(err)
		}
		t.Symbol = instrument.Symbol
		t.NetAmount = models.NetAmount(t)

		if err := tx.Create(&t).Error; err != nil {
			return err
		}
		if err := syncPosition(tx, t.UserID, t.InstrumentID, &t); err != nil {
			return err
		}
		// Reload rather than mirror what syncPosition stamped: the entry may
		// have been back-dated into the middle of history, in which case the
		// value written is one the replay computed and not one this call has.
		return tx.First(&t, "id = ?", t.ID).Error
	})
	if err != nil {
		return models.Transaction{}, err
	}
	return t, nil
}

// GetTransaction returns one of userID's transactions by ID, or ErrNotFound.
// Scoping the query by owner means another user's ID is indistinguishable from a
// nonexistent one, so IDs cannot be probed.
func (d *DB) GetTransaction(id, userID string) (models.Transaction, error) {
	var t models.Transaction
	if err := d.db.First(&t, "id = ? AND user_id = ?", id, userID).Error; err != nil {
		return models.Transaction{}, translate(err)
	}
	return t, nil
}

// TransactionFilter narrows a ListTransactions query. UserID is required — a
// ledger is always read as somebody's. Other zero-value fields are ignored.
type TransactionFilter struct {
	UserID       string
	InstrumentID string
	Side         models.TransactionSide
	From         *time.Time
	To           *time.Time
}

// ListTransactions returns a page of transactions matching filter, plus the
// total count of matches. Defaults to most-recently-traded first.
func (d *DB) ListTransactions(opts ListOptions, filter TransactionFilter) ([]models.Transaction, int64, error) {
	apply := func(q *gorm.DB) *gorm.DB {
		q = q.Where("user_id = ?", filter.UserID)
		if filter.InstrumentID != "" {
			q = q.Where("instrument_id = ?", filter.InstrumentID)
		}
		if filter.Side != "" {
			q = q.Where("side = ?", filter.Side)
		}
		if filter.From != nil {
			q = q.Where("traded_at >= ?", *filter.From)
		}
		if filter.To != nil {
			q = q.Where("traded_at <= ?", *filter.To)
		}
		return q
	}

	var total int64
	if err := apply(d.db.Model(&models.Transaction{})).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	items := []models.Transaction{}
	q := apply(d.db.Model(&models.Transaction{})).
		Order(orderClause(opts, transactionSortColumns, "traded_at", "desc")).
		Offset(opts.Offset)
	if opts.Limit > 0 {
		q = q.Limit(opts.Limit)
	}
	if err := q.Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// TransactionUpdate carries the editable fields of a ledger entry. Side and
// instrument are deliberately not editable: changing either would move the entry
// between positions, which is clearer expressed as a delete plus a re-entry.
type TransactionUpdate struct {
	Quantity int
	Price    int64
	Fee      int64
	TradedAt time.Time
	Note     string
}

// UpdateTransaction edits one of userID's transactions and rebuilds the affected
// position from the ledger. Because the edit can land anywhere in history, the
// position is always fully replayed rather than adjusted incrementally.
//
// It returns models.ErrInsufficientShares when the edit would make some later
// sell exceed the holding — for instance shrinking a buy that a later sell drew
// on. Nothing is written in that case.
func (d *DB) UpdateTransaction(id, userID string, u TransactionUpdate) (models.Transaction, error) {
	var updated models.Transaction
	err := d.db.Transaction(func(tx *gorm.DB) error {
		var existing models.Transaction
		if err := tx.First(&existing, "id = ? AND user_id = ?", id, userID).Error; err != nil {
			return translate(err)
		}

		existing.Quantity = u.Quantity
		existing.Price = u.Price
		existing.Fee = u.Fee
		existing.TradedAt = u.TradedAt
		existing.Note = u.Note
		existing.NetAmount = models.NetAmount(existing)

		if err := tx.Model(&models.Transaction{}).Where("id = ?", id).Updates(map[string]any{
			"quantity":   existing.Quantity,
			"price":      existing.Price,
			"fee":        existing.Fee,
			"traded_at":  existing.TradedAt,
			"note":       existing.Note,
			"net_amount": existing.NetAmount,
		}).Error; err != nil {
			return err
		}

		if err := syncPosition(tx, userID, existing.InstrumentID, nil); err != nil {
			return err
		}
		// The replay restamps this entry and every later sell, so the row is
		// read back rather than assembled from what went in.
		return tx.First(&updated, "id = ?", id).Error
	})
	if err != nil {
		return models.Transaction{}, err
	}
	return updated, nil
}

// DeleteTransaction removes one of userID's transactions and rebuilds the
// affected position from what remains.
//
// It returns models.ErrInsufficientShares when the removal would leave a later
// sell without enough shares behind it — deleting a buy that a later sell drew
// on punches a hole in history. The whole operation rolls back in that case, so
// the ledger is never left in a state its own rules reject.
func (d *DB) DeleteTransaction(id, userID string) error {
	return d.db.Transaction(func(tx *gorm.DB) error {
		var existing models.Transaction
		if err := tx.First(&existing, "id = ? AND user_id = ?", id, userID).Error; err != nil {
			return translate(err)
		}
		if err := tx.Delete(&models.Transaction{}, "id = ?", id).Error; err != nil {
			return err
		}
		return syncPosition(tx, userID, existing.InstrumentID, nil)
	})
}

// ReplayPosition folds a user's whole ledger for one instrument and returns the
// resulting state without touching the stored position. It is the authoritative
// definition of what a position means, and doubles as the oracle the tests check
// the materialized rows against.
func (d *DB) ReplayPosition(userID, instrumentID string) (models.PositionState, error) {
	state, _, err := foldLedger(d.db, userID, instrumentID)
	return state, err
}

// ledgerEntry pairs a folded transaction with the realized profit or loss its
// own step produced — the difference the entry made to the running total, which
// the fold otherwise discards. It is nil for a buy, which realizes nothing.
type ledgerEntry struct {
	tx       models.Transaction
	realized *int64
}

// foldLedger folds the ledger for (userID, instrumentID) in ledgerOrder,
// returning the final state and what each entry contributed to it. The two
// results come from one pass because they must: a per-entry number computed
// against any prefix other than the one the fold actually walked would not sum
// back to the total.
func foldLedger(tx *gorm.DB, userID, instrumentID string) (models.PositionState, []ledgerEntry, error) {
	var txs []models.Transaction
	if err := tx.Where("user_id = ? AND instrument_id = ?", userID, instrumentID).
		Order(ledgerOrder).Find(&txs).Error; err != nil {
		return models.PositionState{}, nil, err
	}

	var state models.PositionState
	entries := make([]ledgerEntry, 0, len(txs))
	for _, t := range txs {
		next, err := state.Apply(t)
		if err != nil {
			// Name the offending entry: after a delete or an edit this is the
			// entry that no longer has enough shares behind it, which is the
			// only actionable thing we can tell the user.
			return models.PositionState{}, nil, fmt.Errorf("%w: %s %d %s on %s",
				err, t.Side, t.Quantity, t.Symbol, t.TradedAt.Format(time.DateOnly))
		}
		entries = append(entries, ledgerEntry{tx: t, realized: realizedStep(state, next, t)})
		state = next
	}
	return state, entries, nil
}

// realizedStep is what one fold step banked: the movement in the running
// realized total across a single Apply — a sale's profit or loss, or a
// dividend's payout. Deriving it by subtraction rather than recomputing it from
// quantity and price keeps models.PositionState.Apply the only place that
// algebra lives — a second answer to "what did this entry earn?" would be free
// to drift from the one maintaining the position.
func realizedStep(before, after models.PositionState, t models.Transaction) *int64 {
	if !t.Side.Realizes() {
		return nil
	}
	step := after.RealizedPL - before.RealizedPL
	return &step
}

// stampRealized writes one entry's realized profit or loss onto its row. The map
// form is deliberate: a nil must reach the column as NULL rather than being
// skipped as a zero value, which is how a buy keeps its "realized nothing".
func stampRealized(tx *gorm.DB, id string, realized *int64) error {
	return tx.Model(&models.Transaction{}).Where("id = ?", id).
		Updates(map[string]any{"realized_pl": realized}).Error
}

// restampLedger brings every entry's stored stamp back in line with the fold it
// came from, skipping rows already holding the right value.
//
// Restamping the whole ledger rather than just the edited row is not
// thoroughness for its own sake: moving-average cost is order-dependent, so
// changing one entry changes what every later sell released, and leaving those
// rows alone would leave the report disagreeing with the position it is a
// decomposition of.
func restampLedger(tx *gorm.DB, entries []ledgerEntry) error {
	for _, e := range entries {
		if equalRealized(e.tx.RealizedPL, e.realized) {
			continue
		}
		if err := stampRealized(tx, e.tx.ID, e.realized); err != nil {
			return err
		}
	}
	return nil
}

// equalRealized compares two stamps, treating nil as its own value rather than
// as zero.
func equalRealized(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// syncPosition brings the stored position for (userID, instrumentID) back in
// line with the ledger.
//
// When appended is non-nil and is the last entry in ledger order, the new state
// is derived by applying just that entry to the current row. Otherwise — an
// edit, a delete, or a back-dated entry that lands mid-history — the whole
// ledger is replayed, because moving-average cost is order-dependent and an
// incremental step would compute the wrong basis.
//
// Either way the write is a compare-and-swap against the values just read, so a
// concurrent writer cannot be silently overwritten, and either way the entries
// the fold walked are stamped with what they realized.
func syncPosition(tx *gorm.DB, userID, instrumentID string, appended *models.Transaction) error {
	current, found, err := loadPosition(tx, userID, instrumentID)
	if err != nil {
		return err
	}

	var state models.PositionState
	incremental := false
	if appended != nil {
		latest, err := isLastInLedger(tx, *appended)
		if err != nil {
			return err
		}
		incremental = latest
	}

	if incremental {
		before := models.PositionState{
			Quantity:   current.Quantity,
			CostBasis:  current.CostBasis,
			RealizedPL: current.RealizedPL,
		}
		state, err = before.Apply(*appended)
		if err != nil {
			return err
		}
		// The row was written a moment ago with no stamp, so only one that
		// realized something needs writing; a buy is already correctly nil.
		if stamp := realizedStep(before, state, *appended); stamp != nil {
			if err := stampRealized(tx, appended.ID, stamp); err != nil {
				return err
			}
		}
	} else {
		var entries []ledgerEntry
		state, entries, err = foldLedger(tx, userID, instrumentID)
		if err != nil {
			return err
		}
		if err := restampLedger(tx, entries); err != nil {
			return err
		}
	}

	return writePosition(tx, current, found, state)
}

// isLastInLedger reports whether t sorts after every other transaction for the
// same user and instrument. A "no" means the entry was back-dated into the
// middle of history and the position must be replayed rather than stepped.
func isLastInLedger(tx *gorm.DB, t models.Transaction) (bool, error) {
	var after int64
	err := tx.Model(&models.Transaction{}).
		Where("user_id = ? AND instrument_id = ?", t.UserID, t.InstrumentID).
		Where(`traded_at > ?
			OR (traded_at = ? AND created_at > ?)
			OR (traded_at = ? AND created_at = ? AND id > ?)`,
			t.TradedAt,
			t.TradedAt, t.CreatedAt,
			t.TradedAt, t.CreatedAt, t.ID).
		Count(&after).Error
	if err != nil {
		return false, err
	}
	return after == 0, nil
}

// loadPosition reads the stored position for a holding. The second result
// reports whether a row exists; a missing row is an empty position, not an error.
func loadPosition(tx *gorm.DB, userID, instrumentID string) (models.Position, bool, error) {
	var p models.Position
	err := tx.First(&p, "user_id = ? AND instrument_id = ?", userID, instrumentID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.Position{UserID: userID, InstrumentID: instrumentID}, false, nil
	}
	if err != nil {
		return models.Position{}, false, err
	}
	return p, true, nil
}

// writePosition stores state, guarding against a concurrent writer.
//
// For an existing row the UPDATE is conditioned on the quantity, cost basis and
// realized total that were read a moment ago — that triple is the row's version.
// If any of them moved, no row matches, and the caller gets ErrConflict instead
// of quietly clobbering the other write.
//
// A plain conditional UPDATE like the one an inventory decrement can use
// (SET stock = stock - ? WHERE stock >= ?) is not available here: a sell
// releases cost in proportion to the shares leaving, so the new value depends on
// the old one in a way SQL arithmetic cannot express. The compare-and-swap is
// what buys back that safety.
//
// For a missing row the INSERT relies on the unique index over
// (user_id, instrument_id): if a concurrent writer inserted the same holding
// first, the duplicate key is the same lost-race signal.
func writePosition(tx *gorm.DB, current models.Position, found bool, state models.PositionState) error {
	if !found {
		p := models.Position{
			ID:           uuid.NewString(),
			UserID:       current.UserID,
			InstrumentID: current.InstrumentID,
			Quantity:     state.Quantity,
			CostBasis:    state.CostBasis,
			RealizedPL:   state.RealizedPL,
		}
		if err := tx.Create(&p).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return ErrConflict
			}
			return err
		}
		return nil
	}

	res := tx.Model(&models.Position{}).
		Where("id = ? AND quantity = ? AND cost_basis = ? AND realized_pl = ?",
			current.ID, current.Quantity, current.CostBasis, current.RealizedPL).
		Updates(map[string]any{
			"quantity":    state.Quantity,
			"cost_basis":  state.CostBasis,
			"realized_pl": state.RealizedPL,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrConflict
	}
	return nil
}
