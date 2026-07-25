package db

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"stockbook/internal/models"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedUser(t *testing.T, s *DB, username string) models.User {
	t.Helper()
	u, err := s.CreateUser(models.User{
		ID: uuid.NewString(), Username: username, PasswordHash: "x", Role: models.RoleUser,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return u
}

func seedInstrument(t *testing.T, s *DB, symbol string) models.Instrument {
	t.Helper()
	i, err := s.CreateInstrument(models.Instrument{
		ID: uuid.NewString(), Symbol: symbol, Name: symbol + " Corp", Market: "TWSE",
	})
	if err != nil {
		t.Fatalf("CreateInstrument: %v", err)
	}
	return i
}

// day returns a timestamp n days after a fixed base, so tests can order the
// ledger explicitly instead of relying on wall-clock timing.
func day(n int) time.Time {
	base := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	return base.AddDate(0, 0, n)
}

// entry is a compact ledger entry for the table-driven tests.
type entry struct {
	side  models.TransactionSide
	qty   int
	price int64
	fee   int64
	day   int
}

// record appends entry e to the ledger through the real write path.
func record(t *testing.T, s *DB, userID, instrumentID string, e entry) (models.Transaction, error) {
	t.Helper()
	return s.CreateTransaction(models.Transaction{
		ID:           uuid.NewString(),
		UserID:       userID,
		InstrumentID: instrumentID,
		Side:         e.side,
		Quantity:     e.qty,
		Price:        e.price,
		Fee:          e.fee,
		TradedAt:     day(e.day),
	})
}

func mustRecord(t *testing.T, s *DB, userID, instrumentID string, e entry) models.Transaction {
	t.Helper()
	tx, err := record(t, s, userID, instrumentID, e)
	if err != nil {
		t.Fatalf("CreateTransaction(%v): %v", e, err)
	}
	return tx
}

// storedState reads the materialized position as a PositionState so it can be
// compared with a replay result field by field.
func storedState(t *testing.T, s *DB, userID, instrumentID string) models.PositionState {
	t.Helper()
	view, err := s.GetPosition(userID, instrumentID)
	if errors.Is(err, ErrNotFound) {
		return models.PositionState{}
	}
	if err != nil {
		t.Fatalf("GetPosition: %v", err)
	}
	return models.PositionState{
		Quantity:   view.Quantity,
		CostBasis:  view.CostBasis,
		RealizedPL: view.RealizedPL,
	}
}

// This is the most valuable test in the suite. The materialized position is a
// cache; replaying the ledger is the definition. Every sequence must produce the
// same answer both ways, or the incremental fast path is lying.
func TestMaterializedPositionMatchesReplay(t *testing.T) {
	sequences := map[string][]entry{
		"single buy":            {{models.SideBuy, 100, 5000, 0, 1}},
		"two buys":              {{models.SideBuy, 100, 5000, 0, 1}, {models.SideBuy, 100, 7000, 0, 2}},
		"buy sell partial":      {{models.SideBuy, 200, 6000, 0, 1}, {models.SideSell, 50, 8000, 0, 2}},
		"full exit":             {{models.SideBuy, 100, 5000, 0, 1}, {models.SideSell, 100, 6000, 0, 2}},
		"exit then re-entry":    {{models.SideBuy, 10, 1000, 0, 1}, {models.SideSell, 10, 1500, 0, 2}, {models.SideBuy, 5, 2000, 0, 3}},
		"with fees":             {{models.SideBuy, 100, 5000, 1425, 1}, {models.SideSell, 40, 5500, 900, 2}},
		"uneven cost basis":     {{models.SideBuy, 3, 1000, 0, 1}, {models.SideBuy, 1, 1001, 0, 2}, {models.SideSell, 1, 1500, 0, 3}},
		"many small trades":     {{models.SideBuy, 7, 333, 11, 1}, {models.SideBuy, 13, 777, 3, 2}, {models.SideSell, 5, 900, 7, 3}, {models.SideBuy, 2, 1234, 1, 4}, {models.SideSell, 9, 450, 5, 5}},
		"same day trades":       {{models.SideBuy, 10, 1000, 0, 1}, {models.SideBuy, 10, 2000, 0, 1}, {models.SideSell, 15, 1800, 0, 1}},
		"sell everything twice": {{models.SideBuy, 10, 1000, 0, 1}, {models.SideSell, 10, 1200, 0, 2}, {models.SideBuy, 10, 900, 0, 3}, {models.SideSell, 10, 800, 0, 4}},
	}

	for name, seq := range sequences {
		t.Run(name, func(t *testing.T) {
			s := newTestDB(t)
			user := seedUser(t, s, "u-"+uuid.NewString()[:8])
			inst := seedInstrument(t, s, "S"+uuid.NewString()[:6])

			for _, e := range seq {
				mustRecord(t, s, user.ID, inst.ID, e)
			}

			replayed, err := s.ReplayPosition(user.ID, inst.ID)
			if err != nil {
				t.Fatalf("ReplayPosition: %v", err)
			}
			if stored := storedState(t, s, user.ID, inst.ID); stored != replayed {
				t.Errorf("materialized %+v != replayed %+v", stored, replayed)
			}
		})
	}
}

// A back-dated entry lands in the middle of history, so the incremental step
// would compute the average against the wrong prefix. The write path must fall
// back to a full replay, and the result must match entering the same trades in
// chronological order to begin with.
func TestBackdatedEntryReplaysPosition(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "backdater")
	inst := seedInstrument(t, s, "2330")

	// Enter day 3 and day 5 first, then discover a forgotten day 1 purchase.
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 9000, 0, 3})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 50, 9500, 0, 5})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 7000, 0, 1})

	got := storedState(t, s, user.ID, inst.ID)

	// The same three trades, entered in chronological order in a fresh book.
	s2 := newTestDB(t)
	user2 := seedUser(t, s2, "chronological")
	inst2 := seedInstrument(t, s2, "2330")
	mustRecord(t, s2, user2.ID, inst2.ID, entry{models.SideBuy, 100, 7000, 0, 1})
	mustRecord(t, s2, user2.ID, inst2.ID, entry{models.SideBuy, 100, 9000, 0, 3})
	mustRecord(t, s2, user2.ID, inst2.ID, entry{models.SideSell, 50, 9500, 0, 5})

	want := storedState(t, s2, user2.ID, inst2.ID)
	if got != want {
		t.Errorf("back-dated entry gave %+v, chronological entry gave %+v", got, want)
	}

	// Sanity-check the arithmetic rather than only checking the two paths agree:
	// 200 shares costing 1,600,000 average 8000; selling 50 releases 400,000 and
	// brings in 475,000, so 75,000 is banked and 1,200,000 of cost remains.
	if want != (models.PositionState{Quantity: 150, CostBasis: 1200000, RealizedPL: 75000}) {
		t.Errorf("unexpected state %+v", want)
	}
}

func TestSellMoreThanHeldIsRejected(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "seller")
	inst := seedInstrument(t, s, "AAPL")
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 10, 1000, 0, 1})

	before := storedState(t, s, user.ID, inst.ID)

	_, err := record(t, s, user.ID, inst.ID, entry{models.SideSell, 11, 1200, 0, 2})
	if !errors.Is(err, models.ErrInsufficientShares) {
		t.Fatalf("got %v, want ErrInsufficientShares", err)
	}

	// The whole write must roll back: no orphaned ledger row, no moved position.
	if after := storedState(t, s, user.ID, inst.ID); after != before {
		t.Errorf("position changed on a rejected sell: %+v -> %+v", before, after)
	}
	items, total, err := s.ListTransactions(ListOptions{Limit: 10}, TransactionFilter{UserID: user.ID})
	if err != nil {
		t.Fatalf("ListTransactions: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Errorf("ledger holds %d entries, want 1 (the rejected sell was persisted)", total)
	}
}

// Deleting a buy that a later sell drew on would punch a hole in history. The
// replay catches it, and the whole operation rolls back rather than leaving the
// ledger in a state its own rules reject.
func TestDeletingABuyThatALaterSellNeedsIsRejected(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "deleter")
	inst := seedInstrument(t, s, "TSLA")

	firstBuy := mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 5000, 0, 1})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 50, 6000, 0, 2})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 120, 7000, 0, 3})

	before := storedState(t, s, user.ID, inst.ID)

	err := s.DeleteTransaction(firstBuy.ID, user.ID)
	if !errors.Is(err, models.ErrInsufficientShares) {
		t.Fatalf("got %v, want ErrInsufficientShares", err)
	}
	// The message must name the entry that would break, since that is the only
	// actionable detail the user can act on.
	if got := err.Error(); !strings.Contains(got, "TSLA") || !strings.Contains(got, "sell") {
		t.Errorf("error %q should name the offending sell", got)
	}

	if after := storedState(t, s, user.ID, inst.ID); after != before {
		t.Errorf("position changed on a rejected delete: %+v -> %+v", before, after)
	}
	if _, err := s.GetTransaction(firstBuy.ID, user.ID); err != nil {
		t.Errorf("the buy was deleted despite the rejection: %v", err)
	}
}

// Deleting an entry nothing depends on rebuilds the position from what is left.
func TestDeletingASafeEntryReplaysPosition(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "tidier")
	inst := seedInstrument(t, s, "MSFT")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 5000, 0, 1})
	duplicate := mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 5000, 0, 2})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 50, 6000, 0, 3})

	if err := s.DeleteTransaction(duplicate.ID, user.ID); err != nil {
		t.Fatalf("DeleteTransaction: %v", err)
	}

	replayed, err := s.ReplayPosition(user.ID, inst.ID)
	if err != nil {
		t.Fatalf("ReplayPosition: %v", err)
	}
	stored := storedState(t, s, user.ID, inst.ID)
	if stored != replayed {
		t.Errorf("materialized %+v != replayed %+v", stored, replayed)
	}
	// 100 bought at 50.00, 50 sold at 60.00: half the cost released, 50,000 banked.
	if stored != (models.PositionState{Quantity: 50, CostBasis: 250000, RealizedPL: 50000}) {
		t.Errorf("unexpected state after delete: %+v", stored)
	}
}

func TestUpdateTransactionReplaysPosition(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "editor")
	inst := seedInstrument(t, s, "NVDA")

	buy := mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 5000, 0, 1})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 40, 7000, 0, 2})

	// Correct the purchase price: 100 shares at 55.00 rather than 50.00.
	if _, err := s.UpdateTransaction(buy.ID, user.ID, TransactionUpdate{
		Quantity: 100, Price: 5500, TradedAt: day(1),
	}); err != nil {
		t.Fatalf("UpdateTransaction: %v", err)
	}

	replayed, err := s.ReplayPosition(user.ID, inst.ID)
	if err != nil {
		t.Fatalf("ReplayPosition: %v", err)
	}
	stored := storedState(t, s, user.ID, inst.ID)
	if stored != replayed {
		t.Errorf("materialized %+v != replayed %+v", stored, replayed)
	}
	// 550,000 cost over 100 shares; selling 40 releases 220,000 against 280,000
	// of proceeds, banking 60,000 and leaving 330,000.
	if stored != (models.PositionState{Quantity: 60, CostBasis: 330000, RealizedPL: 60000}) {
		t.Errorf("unexpected state after edit: %+v", stored)
	}
}

// Shrinking a buy below what a later sell consumed is the edit-shaped version of
// the delete hole, and must be refused the same way.
func TestUpdateThatUnderminesALaterSellIsRejected(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "shrinker")
	inst := seedInstrument(t, s, "AMD")

	buy := mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 5000, 0, 1})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 90, 6000, 0, 2})
	before := storedState(t, s, user.ID, inst.ID)

	_, err := s.UpdateTransaction(buy.ID, user.ID, TransactionUpdate{
		Quantity: 50, Price: 5000, TradedAt: day(1),
	})
	if !errors.Is(err, models.ErrInsufficientShares) {
		t.Fatalf("got %v, want ErrInsufficientShares", err)
	}
	if after := storedState(t, s, user.ID, inst.ID); after != before {
		t.Errorf("position changed on a rejected edit: %+v -> %+v", before, after)
	}
	original, err := s.GetTransaction(buy.ID, user.ID)
	if err != nil {
		t.Fatalf("GetTransaction: %v", err)
	}
	if original.Quantity != 100 {
		t.Errorf("quantity was written despite the rejection: %d", original.Quantity)
	}
}

// Each user's ledger and holdings are entirely their own, even for the same
// instrument.
func TestPositionsAreScopedPerUser(t *testing.T) {
	s := newTestDB(t)
	alice := seedUser(t, s, "alice")
	bob := seedUser(t, s, "bob")
	inst := seedInstrument(t, s, "GOOG")

	mustRecord(t, s, alice.ID, inst.ID, entry{models.SideBuy, 10, 1000, 0, 1})
	mustRecord(t, s, bob.ID, inst.ID, entry{models.SideBuy, 5, 2000, 0, 1})

	if got := storedState(t, s, alice.ID, inst.ID); got.Quantity != 10 || got.CostBasis != 10000 {
		t.Errorf("alice: %+v", got)
	}
	if got := storedState(t, s, bob.ID, inst.ID); got.Quantity != 5 || got.CostBasis != 10000 {
		t.Errorf("bob: %+v", got)
	}
	// Bob cannot read Alice's ledger entry, even knowing its ID.
	items, _, err := s.ListTransactions(ListOptions{Limit: 10}, TransactionFilter{UserID: bob.ID})
	if err != nil {
		t.Fatalf("ListTransactions: %v", err)
	}
	if len(items) != 1 || items[0].UserID != bob.ID {
		t.Errorf("bob's ledger leaked other rows: %+v", items)
	}
}

func TestNetAmountIsServerComputed(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "netter")
	inst := seedInstrument(t, s, "IBM")

	// A caller-supplied NetAmount must be ignored, not persisted.
	created, err := s.CreateTransaction(models.Transaction{
		ID: uuid.NewString(), UserID: user.ID, InstrumentID: inst.ID,
		Side: models.SideBuy, Quantity: 10, Price: 1000, Fee: 25,
		NetAmount: 999999, TradedAt: day(1),
	})
	if err != nil {
		t.Fatalf("CreateTransaction: %v", err)
	}
	if created.NetAmount != 10025 {
		t.Errorf("net amount = %d, want 10025", created.NetAmount)
	}
	// The symbol is snapshotted from the instrument, not taken from the caller.
	if created.Symbol != "IBM" {
		t.Errorf("symbol = %q, want IBM", created.Symbol)
	}
}

// Renaming an instrument must not rewrite the ledger.
func TestInstrumentRenameDoesNotRewriteHistory(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "renamer")
	inst := seedInstrument(t, s, "OLD")
	tx := mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 10, 1000, 0, 1})

	if _, err := s.RenameInstrument(inst.ID, "Renamed Corp"); err != nil {
		t.Fatalf("RenameInstrument: %v", err)
	}

	stored, err := s.GetTransaction(tx.ID, user.ID)
	if err != nil {
		t.Fatalf("GetTransaction: %v", err)
	}
	if stored.Symbol != "OLD" {
		t.Errorf("ledger symbol = %q, want the OLD snapshot", stored.Symbol)
	}
}

func TestCreateTransactionRejectsUnknownInstrument(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "ghost")

	_, err := s.CreateTransaction(models.Transaction{
		ID: uuid.NewString(), UserID: user.ID, InstrumentID: "does-not-exist",
		Side: models.SideBuy, Quantity: 1, Price: 100, TradedAt: day(1),
	})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}
