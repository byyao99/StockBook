package db

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"stockbook/internal/models"
)

// The concurrency tests at the handler level pass partly because SQLite is
// configured with a single connection, which serializes writers. These tests
// drive writePosition directly to prove the compare-and-swap itself refuses a
// stale write — the guard that keeps the write path correct on an engine that
// does let writers run in parallel.

func TestWritePositionRejectsStaleUpdate(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "racer")
	inst := seedInstrument(t, s, "2330")
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 10, 9000, 0, 1})

	// Read the row the way a writer would before computing its new state.
	stale, found, err := loadPosition(s.db, user.ID, inst.ID)
	if err != nil || !found {
		t.Fatalf("loadPosition: %v (found=%v)", err, found)
	}

	// Another writer moves the position underneath us.
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 5, 10000, 0, 2})

	// Our write is computed from what we read a moment ago, so it must be
	// refused rather than silently discarding the other writer's 5 shares.
	err = writePosition(s.db, stale, found, models.PositionState{Quantity: 99, CostBasis: 1, RealizedPL: 0})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("got %v, want ErrConflict", err)
	}

	// The other writer's result must still be intact.
	after := storedState(t, s, user.ID, inst.ID)
	if after.Quantity != 15 {
		t.Errorf("quantity = %d, want 15 (the stale write clobbered it)", after.Quantity)
	}
}

// The realized total is part of the version, not just quantity and cost: two
// writers can agree on the shares held and still disagree about what was banked.
func TestWritePositionGuardsRealizedTotal(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "realizer")
	inst := seedInstrument(t, s, "2330")
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 10, 9000, 0, 1})

	stale, found, err := loadPosition(s.db, user.ID, inst.ID)
	if err != nil || !found {
		t.Fatalf("loadPosition: %v", err)
	}

	// Move only the realized total, leaving quantity and cost untouched.
	if err := s.db.Model(&models.Position{}).Where("id = ?", stale.ID).
		Update("realized_pl", 12345).Error; err != nil {
		t.Fatalf("concurrent update: %v", err)
	}

	err = writePosition(s.db, stale, found, models.PositionState{
		Quantity: stale.Quantity, CostBasis: stale.CostBasis, RealizedPL: 0,
	})
	if !errors.Is(err, ErrConflict) {
		t.Errorf("got %v, want ErrConflict", err)
	}
}

// A writer that believes the holding does not exist yet must lose to the writer
// that created it first. The unique index over (user_id, instrument_id) is what
// turns that race into a conflict instead of a duplicate holding.
func TestWritePositionRejectsDuplicateInsert(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "duper")
	inst := seedInstrument(t, s, "2330")

	// Both writers observed no position.
	first, found, err := loadPosition(s.db, user.ID, inst.ID)
	if err != nil {
		t.Fatalf("loadPosition: %v", err)
	}
	if found {
		t.Fatal("expected no position yet")
	}
	second := first

	if err := writePosition(s.db, first, false, models.PositionState{Quantity: 10, CostBasis: 90000}); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	err = writePosition(s.db, second, false, models.PositionState{Quantity: 7, CostBasis: 70000})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("second insert: got %v, want ErrConflict", err)
	}

	// Exactly one holding must exist, holding the winner's numbers.
	var count int64
	if err := s.db.Model(&models.Position{}).
		Where("user_id = ? AND instrument_id = ?", user.ID, inst.ID).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("%d position rows, want 1", count)
	}
	if got := storedState(t, s, user.ID, inst.ID); got.Quantity != 10 {
		t.Errorf("quantity = %d, want the winner's 10", got.Quantity)
	}
}

// A fresh read must be able to write, or the guard would block all progress
// rather than only stale writes.
func TestWritePositionAcceptsFreshUpdate(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "fresh")
	inst := seedInstrument(t, s, "2330")
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 10, 9000, 0, 1})

	current, found, err := loadPosition(s.db, user.ID, inst.ID)
	if err != nil || !found {
		t.Fatalf("loadPosition: %v", err)
	}
	want := models.PositionState{Quantity: 20, CostBasis: 180000, RealizedPL: 5}
	if err := writePosition(s.db, current, found, want); err != nil {
		t.Fatalf("writePosition: %v", err)
	}
	if got := storedState(t, s, user.ID, inst.ID); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// The unique index must also hold at the schema level, independently of the
// application-level guard above.
func TestPositionUniqueIndexIsEnforced(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "indexed")
	inst := seedInstrument(t, s, "2330")

	base := models.Position{
		ID: uuid.NewString(), UserID: user.ID, InstrumentID: inst.ID, Quantity: 1,
	}
	if err := s.db.Create(&base).Error; err != nil {
		t.Fatalf("first insert: %v", err)
	}
	twin := base
	twin.ID = uuid.NewString()
	if err := s.db.Create(&twin).Error; err == nil {
		t.Error("a second holding for the same (user, instrument) was accepted")
	}
}

// A currency stamped on an instrument created before the column existed gets
// filled in from its market at startup, so it cannot silently drop out of every
// per-currency total.
func TestOpenBackfillsMissingCurrencies(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/backfill.db"

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	us := seedInstrumentOn(t, s, "TSLA", "NASDAQ")
	tw := seedInstrumentOn(t, s, "2330", "TWSE")

	// Simulate rows written before the column existed.
	if err := s.db.Exec("UPDATE instruments SET currency = ''").Error; err != nil {
		t.Fatalf("clear currencies: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })

	for id, want := range map[string]models.Currency{
		us.ID: models.CurrencyUSD,
		tw.ID: models.CurrencyTWD,
	} {
		item, err := reopened.GetInstrument(id)
		if err != nil {
			t.Fatalf("GetInstrument: %v", err)
		}
		if item.Currency != want {
			t.Errorf("%s currency = %q, want %q backfilled from its market",
				item.Symbol, item.Currency, want)
		}
	}
}

// seedInstrumentOn inserts an instrument on a specific market.
func seedInstrumentOn(t *testing.T, s *DB, symbol, market string) models.Instrument {
	t.Helper()
	item, err := s.CreateInstrument(models.Instrument{
		ID: uuid.NewString(), Symbol: symbol, Name: symbol + " Corp",
		Market: market, Currency: models.DefaultCurrencyForMarket(market),
	})
	if err != nil {
		t.Fatalf("CreateInstrument: %v", err)
	}
	return item
}
