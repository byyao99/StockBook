package db

import (
	"testing"
	"time"

	"stockbook/internal/models"
)

// seedSplit records a split the provider reported, dayOffset days after the
// fixed base the ledger helpers share.
func seedSplit(t *testing.T, s *DB, instrumentID string, dayOffset int, ratioPpm int64) {
	t.Helper()
	err := s.SaveSplitEvents(instrumentID, []models.SplitEvent{{
		Date:     day(dayOffset).UTC().Format(time.DateOnly),
		RatioPpm: ratioPpm,
	}})
	if err != nil {
		t.Fatalf("SaveSplitEvents: %v", err)
	}
}

// The point of the warning. Shares were bought, the company split, and nothing
// in the book says so — which is the state in which every figure spanning the
// date is silently wrong by the ratio.
func TestUnadjustedSplitsFindsOneHeldAcross(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "holder")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 1000, 50_000, 0, 0})
	seedSplit(t, s, inst.ID, 30, 2*models.SplitRatioScale)

	got, err := s.UnadjustedSplits(user.ID, false)
	if err != nil {
		t.Fatalf("UnadjustedSplits: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d splits, want 1: %+v", len(got), got)
	}
	if got[0].Shares != shares(1000) {
		t.Errorf("shares %d, want the 1000 held before the split", got[0].Shares)
	}
	if got[0].RatioPpm != 2*models.SplitRatioScale {
		t.Errorf("ratio %d, want %d", got[0].RatioPpm, 2*models.SplitRatioScale)
	}
	if got[0].Symbol != "2330" {
		t.Errorf("symbol %q, want 2330", got[0].Symbol)
	}
}

// A book that bought in *after* the split holds nothing but post-split shares,
// so there is nothing wrong with it and nothing to warn about. Warning anyway
// is how a reader learns to ignore the one that matters.
func TestUnadjustedSplitsIgnoresOneBoughtIntoAfterwards(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "latecomer")
	inst := seedInstrument(t, s, "2330")

	seedSplit(t, s, inst.ID, 30, 2*models.SplitRatioScale)
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 1000, 25_000, 0, 31})

	got, err := s.UnadjustedSplits(user.ID, false)
	if err != nil {
		t.Fatalf("UnadjustedSplits: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d splits for a book that only bought after one: %+v", len(got), got)
	}
}

// The ex-date is the first session trading in new shares, so a purchase dated
// on it was already recorded in them. Only the session *before* decides.
func TestUnadjustedSplitsIgnoresAPurchaseOnTheExDateItself(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "sameday")
	inst := seedInstrument(t, s, "2330")

	seedSplit(t, s, inst.ID, 30, 2*models.SplitRatioScale)
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 1000, 25_000, 0, 30})

	got, err := s.UnadjustedSplits(user.ID, false)
	if err != nil {
		t.Fatalf("UnadjustedSplits: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d splits for a purchase dated on the ex-date: %+v", len(got), got)
	}
}

// A holding sold since is still warned about: the years it was held are in
// every report on /reports, and the split corrupted all of them. Scoping the
// warning to what is still owned would quietly exempt exactly that history.
func TestUnadjustedSplitsWarnsAboutAHoldingSinceSold(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "seller")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 1000, 50_000, 0, 0})
	seedSplit(t, s, inst.ID, 30, 2*models.SplitRatioScale)
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 1000, 26_000, 0, 60})

	got, err := s.UnadjustedSplits(user.ID, false)
	if err != nil {
		t.Fatalf("UnadjustedSplits: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d splits after the holding was closed, want 1: %+v", len(got), got)
	}
}

// Another user's book is another user's business, exactly as a ledger is
// everywhere else here — even though the split itself is shared market data.
func TestUnadjustedSplitsIsScopedToTheCaller(t *testing.T) {
	s := newTestDB(t)
	holder := seedUser(t, s, "holder")
	other := seedUser(t, s, "other")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, holder.ID, inst.ID, entry{models.SideBuy, 1000, 50_000, 0, 0})
	seedSplit(t, s, inst.ID, 30, 2*models.SplitRatioScale)

	got, err := s.UnadjustedSplits(other.ID, false)
	if err != nil {
		t.Fatalf("UnadjustedSplits: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a user who never held it got %d splits: %+v", len(got), got)
	}
}

// The warning can never resolve itself — shares were held across the date
// forever — so it has to be dismissible, and the dismissal has to be
// reversible, or a misclick silences a genuinely broken history for good.
func TestAcknowledgingASplitHidesItAndCanBeUndone(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "holder")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 1000, 50_000, 0, 0})
	date := day(30).UTC().Format(time.DateOnly)
	seedSplit(t, s, inst.ID, 30, 2*models.SplitRatioScale)

	if err := s.AcknowledgeSplit(user.ID, inst.ID, date); err != nil {
		t.Fatalf("AcknowledgeSplit: %v", err)
	}
	pending, err := s.UnadjustedSplits(user.ID, false)
	if err != nil {
		t.Fatalf("UnadjustedSplits: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("acknowledged split still pending: %+v", pending)
	}

	// It is still reportable when asked for, flagged, so the holding's own page
	// can offer the undo.
	all, err := s.UnadjustedSplits(user.ID, true)
	if err != nil {
		t.Fatalf("UnadjustedSplits(include): %v", err)
	}
	if len(all) != 1 || !all[0].Acknowledged {
		t.Fatalf("want one acknowledged split, got %+v", all)
	}

	if err := s.ForgetSplitAcknowledgement(user.ID, inst.ID, date); err != nil {
		t.Fatalf("ForgetSplitAcknowledgement: %v", err)
	}
	pending, err = s.UnadjustedSplits(user.ID, false)
	if err != nil {
		t.Fatalf("UnadjustedSplits: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("want the warning back after undoing the dismissal, got %+v", pending)
	}
}

// Acknowledging one user's split leaves everybody else's warning standing: the
// dismissal is a decision about one ledger, not about the market.
func TestAcknowledgementIsPerUser(t *testing.T) {
	s := newTestDB(t)
	first := seedUser(t, s, "first")
	second := seedUser(t, s, "second")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, first.ID, inst.ID, entry{models.SideBuy, 1000, 50_000, 0, 0})
	mustRecord(t, s, second.ID, inst.ID, entry{models.SideBuy, 500, 50_000, 0, 0})
	date := day(30).UTC().Format(time.DateOnly)
	seedSplit(t, s, inst.ID, 30, 2*models.SplitRatioScale)

	if err := s.AcknowledgeSplit(first.ID, inst.ID, date); err != nil {
		t.Fatalf("AcknowledgeSplit: %v", err)
	}
	got, err := s.UnadjustedSplits(second.ID, false)
	if err != nil {
		t.Fatalf("UnadjustedSplits: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("another user's dismissal hid the warning: %+v", got)
	}
}

// Acknowledging a split nothing knows about would store a row that silences
// nothing and report success — a no-op discovered only when the warning it was
// meant to dismiss comes back.
func TestAcknowledgingAnUnknownSplitIsNotFound(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "holder")
	inst := seedInstrument(t, s, "2330")

	err := s.AcknowledgeSplit(user.ID, inst.ID, "2026-02-01")
	if err != ErrNotFound {
		t.Errorf("AcknowledgeSplit for an unknown event: %v, want ErrNotFound", err)
	}
}

// A refetch overlaps what is already stored on purpose, so the same event
// arrives more than once and has to land in one row with the latest ratio.
func TestSavingASplitTwiceUpdatesInPlace(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "holder")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 1000, 50_000, 0, 0})
	seedSplit(t, s, inst.ID, 30, 2*models.SplitRatioScale)
	seedSplit(t, s, inst.ID, 30, 4*models.SplitRatioScale)

	got, err := s.UnadjustedSplits(user.ID, false)
	if err != nil {
		t.Fatalf("UnadjustedSplits: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows for one refetched split: %+v", len(got), got)
	}
	if got[0].RatioPpm != 4*models.SplitRatioScale {
		t.Errorf("ratio %d, want the revised %d", got[0].RatioPpm, 4*models.SplitRatioScale)
	}
}
