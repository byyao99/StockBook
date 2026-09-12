package db

import (
	"testing"
	"time"

	"stockbook/internal/models"
)

// seedDividend records a distribution the provider reported.
func seedDividend(t *testing.T, s *DB, instrumentID string, dayOffset int, perShare int64) {
	t.Helper()
	err := s.SaveDividendEvents(instrumentID, []models.DividendEvent{{
		ExDate: day(dayOffset).UTC().Format(time.DateOnly),
		Amount: perShare,
	}})
	if err != nil {
		t.Fatalf("SaveDividendEvents: %v", err)
	}
}

// The point of the report: shares were held on the ex-date and nothing was ever
// written down, which is how a dividend actually goes missing.
func TestPendingDividendsFindsAnUnrecordedPayout(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "holder")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 1000, 50_000, 0, 0})
	seedDividend(t, s, inst.ID, 30, 400)

	pending, err := s.PendingDividends(user.ID)
	if err != nil {
		t.Fatalf("PendingDividends: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("got %d pending, want 1: %+v", len(pending), pending)
	}
	got := pending[0]
	if got.Shares != shares(1000) {
		t.Errorf("shares %d, want 1000", got.Shares)
	}
	// 1000 shares at NT$4.00 is NT$4,000, before whatever was withheld.
	if got.Estimated != 400_000 {
		t.Errorf("estimated %d, want 400000", got.Estimated)
	}
}

// Once it is recorded it stops being pending, even though the entry is dated
// weeks after the ex-date — which is when the cash actually arrives.
func TestPendingDividendsClearsOnceRecorded(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "recorder")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 1000, 50_000, 0, 0})
	seedDividend(t, s, inst.ID, 30, 400)
	// Paid out a month after the ex-date, which is the ordinary lag.
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideDividend, 1000, 400, 0, 60})

	pending, err := s.PendingDividends(user.ID)
	if err != nil {
		t.Fatalf("PendingDividends: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("got %d pending after recording it: %+v", len(pending), pending)
	}
}

// One recorded entry answers for one distribution. A quarterly payer recorded
// once must still show the quarters that were not — the failure this whole
// report exists to catch.
func TestPendingDividendsDoesNotLetOneEntryAnswerForTwoQuarters(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "quarterly")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 1000, 50_000, 0, 0})
	seedDividend(t, s, inst.ID, 30, 400)
	seedDividend(t, s, inst.ID, 120, 400)
	// Only the first was ever written down.
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideDividend, 1000, 400, 0, 60})

	pending, err := s.PendingDividends(user.ID)
	if err != nil {
		t.Fatalf("PendingDividends: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("got %d pending, want the second quarter only: %+v", len(pending), pending)
	}
	if want := day(120).UTC().Format(time.DateOnly); pending[0].ExDate != want {
		t.Errorf("pending ex-date %s, want %s", pending[0].ExDate, want)
	}
}

// A distribution from before the shares were bought is somebody else's.
func TestPendingDividendsIgnoresPayoutsBeforeTheSharesWereHeld(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "latecomer")
	inst := seedInstrument(t, s, "2330")

	seedDividend(t, s, inst.ID, 10, 400)
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 1000, 50_000, 0, 50})

	pending, err := s.PendingDividends(user.ID)
	if err != nil {
		t.Fatalf("PendingDividends: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("claimed a payout from before the holding existed: %+v", pending)
	}
}

// Entitlement is decided on the ex-date, not today. Shares sold since were still
// owed the payout, and refusing to prompt for it would lose real money — the same
// reason a dividend entry is never checked against shares on hand.
func TestPendingDividendsCountsSharesHeldOnTheExDateNotNow(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "seller")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 1000, 50_000, 0, 0})
	seedDividend(t, s, inst.ID, 30, 400)
	// Sold out entirely after the ex-date.
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 1000, 60_000, 0, 40})

	pending, err := s.PendingDividends(user.ID)
	if err != nil {
		t.Fatalf("PendingDividends: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("got %d pending, want the payout the sold shares still earned: %+v",
			len(pending), pending)
	}
	if pending[0].Shares != shares(1000) {
		t.Errorf("shares %d, want the 1000 held on the ex-date", pending[0].Shares)
	}
}

// A distribution is master data; which of them anybody is owed is not.
func TestPendingDividendsIsScopedToTheCaller(t *testing.T) {
	s := newTestDB(t)
	mine := seedUser(t, s, "mine")
	theirs := seedUser(t, s, "theirs")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, theirs.ID, inst.ID, entry{models.SideBuy, 1000, 50_000, 0, 0})
	seedDividend(t, s, inst.ID, 30, 400)

	pending, err := s.PendingDividends(mine.ID)
	if err != nil {
		t.Fatalf("PendingDividends: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("saw another book's entitlement: %+v", pending)
	}
}

// A refetch overlaps what is stored on purpose, and a provider does revise an
// amount, so the same ex-date must update rather than duplicate.
func TestDividendEventRefetchIsIdempotent(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "syncer")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 1000, 50_000, 0, 0})
	seedDividend(t, s, inst.ID, 30, 400)
	seedDividend(t, s, inst.ID, 30, 450)

	pending, err := s.PendingDividends(user.ID)
	if err != nil {
		t.Fatalf("PendingDividends: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("got %d rows for one distribution: %+v", len(pending), pending)
	}
	if pending[0].PerShare != 450 {
		t.Errorf("per share %d, want the revised 450", pending[0].PerShare)
	}
}
