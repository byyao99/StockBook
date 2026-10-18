package db

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"stockbook/internal/models"
)

func seedPlan(t *testing.T, s *DB, userID, instrumentID, days string, amount int64, startDay int) models.RecurringPlan {
	t.Helper()
	plan, err := s.CreatePlan(models.RecurringPlan{
		ID:           uuid.NewString(),
		UserID:       userID,
		InstrumentID: instrumentID,
		DaysOfMonth:  days,
		Amount:       amount,
		StartedOn:    day(startDay).UTC().Format(time.DateOnly),
	})
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	return plan
}

func TestParsePlanDays(t *testing.T) {
	got, err := ParsePlanDays(" 25, 5 ,15,5 ")
	if err != nil {
		t.Fatalf("ParsePlanDays: %v", err)
	}
	// Sorted and deduplicated, so the stored form is canonical however it was typed.
	if FormatPlanDays(got) != "5,15,25" {
		t.Errorf("got %q, want \"5,15,25\"", FormatPlanDays(got))
	}
	for _, bad := range []string{"", "0", "32", "abc", "-3"} {
		if _, err := ParsePlanDays(bad); err == nil {
			t.Errorf("ParsePlanDays(%q) was accepted", bad)
		}
	}
}

// A day past the end of a short month clamps rather than being skipped. A plan
// for the 31st is a plan to buy monthly, and dropping February would quietly
// make it eleven purchases a year.
func TestDueDatesClampToShortMonths(t *testing.T) {
	plan := models.RecurringPlan{
		DaysOfMonth: "31",
		StartedOn:   "2026-01-31",
	}
	dates, err := dueDates(plan, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("dueDates: %v", err)
	}
	want := []string{"2026-01-31", "2026-02-28", "2026-03-31"}
	if len(dates) != len(want) {
		t.Fatalf("got %v, want %v", dates, want)
	}
	for i, d := range want {
		if dates[i] != d {
			t.Errorf("date %d = %s, want %s", i, dates[i], d)
		}
	}
}

// The point of the report: the plan ran and nothing was written down.
func TestPendingPurchasesFindsMissedInstalments(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "saver")
	inst := seedInstrument(t, s, "2330")
	seedPlan(t, s, user.ID, inst.ID, "5,15,25", 300_000, 0)
	closesFrom(t, s, inst.ID, 0, 50_000)

	pending, err := s.PendingPurchases(user.ID, day(40))
	if err != nil {
		t.Fatalf("PendingPurchases: %v", err)
	}
	if len(pending) == 0 {
		t.Fatal("a plan that has been running a month reported nothing due")
	}
	// NT$3,000 at NT$500.00 buys 6 shares exactly.
	if pending[0].EstimatedShares == nil || *pending[0].EstimatedShares != shares(6) {
		t.Errorf("estimated shares %v, want %d", pending[0].EstimatedShares, shares(6))
	}
	if pending[0].Amount != 300_000 {
		t.Errorf("amount %d, want the plan's", pending[0].Amount)
	}
}

// Recording one instalment clears exactly one.
func TestPendingPurchasesClearsOnceRecorded(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "saver")
	inst := seedInstrument(t, s, "2330")
	seedPlan(t, s, user.ID, inst.ID, "5", 300_000, 0)
	closesFrom(t, s, inst.ID, 0, 50_000)

	before, err := s.PendingPurchases(user.ID, day(70))
	if err != nil {
		t.Fatalf("PendingPurchases: %v", err)
	}
	// Record the oldest instalment, dated a couple of days after it was due.
	due, _ := time.Parse(time.DateOnly, before[len(before)-1].DueOn)
	if _, err := s.CreateTransaction(models.Transaction{
		ID: uuid.NewString(), UserID: user.ID, InstrumentID: inst.ID,
		Side: models.SideBuy, Quantity: shares(6), Price: 50_000,
		TradedAt: due.AddDate(0, 0, 2),
	}); err != nil {
		t.Fatalf("CreateTransaction: %v", err)
	}

	after, err := s.PendingPurchases(user.ID, day(70))
	if err != nil {
		t.Fatalf("PendingPurchases: %v", err)
	}
	if len(after) != len(before)-1 {
		t.Errorf("got %d pending after recording one, want %d", len(after), len(before)-1)
	}
}

// One buy answers for one instalment. A monthly plan recorded once must still
// show the months that were not — the failure this report exists to catch.
func TestOneBuyDoesNotAnswerForTwoInstalments(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "saver")
	inst := seedInstrument(t, s, "2330")
	seedPlan(t, s, user.ID, inst.ID, "5", 300_000, 0)
	closesFrom(t, s, inst.ID, 0, 50_000)

	// Two instalments due, one recorded.
	due, _ := time.Parse(time.DateOnly, day(0).UTC().Format(time.DateOnly))
	if _, err := s.CreateTransaction(models.Transaction{
		ID: uuid.NewString(), UserID: user.ID, InstrumentID: inst.ID,
		Side: models.SideBuy, Quantity: shares(6), Price: 50_000,
		TradedAt: due.AddDate(0, 0, 4),
	}); err != nil {
		t.Fatalf("CreateTransaction: %v", err)
	}

	pending, err := s.PendingPurchases(user.ID, day(70))
	if err != nil {
		t.Fatalf("PendingPurchases: %v", err)
	}
	if len(pending) == 0 {
		t.Fatal("one recorded buy answered for every instalment")
	}
}

// A plan that has ended stops coming due.
func TestAnEndedPlanStopsComingDue(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "saver")
	inst := seedInstrument(t, s, "2330")
	plan := seedPlan(t, s, user.ID, inst.ID, "5", 300_000, 0)
	closesFrom(t, s, inst.ID, 0, 50_000)

	if err := s.EndPlan(user.ID, plan.ID, day(10).UTC().Format(time.DateOnly)); err != nil {
		t.Fatalf("EndPlan: %v", err)
	}
	pending, err := s.PendingPurchases(user.ID, day(200))
	if err != nil {
		t.Fatalf("PendingPurchases: %v", err)
	}
	for _, p := range pending {
		if p.DueOn > day(10).UTC().Format(time.DateOnly) {
			t.Errorf("instalment %s came due after the plan ended", p.DueOn)
		}
	}
}

// Without a stored close there is nothing to estimate from, and an estimate of
// zero shares would be a claim rather than a blank.
func TestPendingPurchaseWithoutAPriceEstimatesNothing(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "saver")
	inst := seedInstrument(t, s, "2330")
	seedPlan(t, s, user.ID, inst.ID, "5", 300_000, 0)

	pending, err := s.PendingPurchases(user.ID, day(40))
	if err != nil {
		t.Fatalf("PendingPurchases: %v", err)
	}
	if len(pending) == 0 {
		t.Fatal("nothing reported due")
	}
	if pending[0].EstimatedShares != nil || pending[0].EstimatedPrice != nil {
		t.Errorf("estimated %v shares at %v with no stored price",
			pending[0].EstimatedShares, pending[0].EstimatedPrice)
	}
}

// A plan is personal, like everything else derived from a book.
func TestPlansAreScopedToTheirOwner(t *testing.T) {
	s := newTestDB(t)
	mine := seedUser(t, s, "mine")
	theirs := seedUser(t, s, "theirs")
	inst := seedInstrument(t, s, "2330")
	seedPlan(t, s, theirs.ID, inst.ID, "5", 300_000, 0)

	plans, err := s.ListPlans(mine.ID)
	if err != nil {
		t.Fatalf("ListPlans: %v", err)
	}
	if len(plans) != 0 {
		t.Errorf("saw another book's plan: %+v", plans)
	}
	pending, err := s.PendingPurchases(mine.ID, day(40))
	if err != nil {
		t.Fatalf("PendingPurchases: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("saw another book's instalments: %+v", pending)
	}
}

// The bug this exists for: a plan set up before its first purchase needs the
// closes to estimate an instalment from, and the history sync used to fetch
// only what had been traded — so an instrument with a plan and no trades was
// skipped, and its prompts could never offer an estimate.
func TestHistoryIsNeededForAPlannedInstrumentThatWasNeverTraded(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "saver")
	inst := seedInstrument(t, s, "VOO")

	needed, err := s.EarliestHistoryNeeded(inst.ID)
	if err != nil {
		t.Fatalf("EarliestHistoryNeeded: %v", err)
	}
	if needed != "" {
		t.Fatalf("an untraded, unplanned instrument needs history from %q", needed)
	}

	plan := seedPlan(t, s, user.ID, inst.ID, "5,15,25", 10_000, 3)
	needed, err = s.EarliestHistoryNeeded(inst.ID)
	if err != nil {
		t.Fatalf("EarliestHistoryNeeded: %v", err)
	}
	if needed != plan.StartedOn {
		t.Errorf("history needed from %q, want the plan's start %q", needed, plan.StartedOn)
	}
}

// A trade before the plan moves the boundary back: the earlier of the two is
// what history has to reach, or the ledger's own opening trade goes unvalued.
func TestHistoryReachesBackToWhicheverCameFirst(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "saver")
	inst := seedInstrument(t, s, "VOO")
	seedPlan(t, s, user.ID, inst.ID, "5", 10_000, 30)

	if _, err := s.CreateTransaction(models.Transaction{
		ID: uuid.NewString(), UserID: user.ID, InstrumentID: inst.ID,
		Side: models.SideBuy, Quantity: shares(1), Price: 70_000,
		TradedAt: day(2),
	}); err != nil {
		t.Fatalf("CreateTransaction: %v", err)
	}

	needed, err := s.EarliestHistoryNeeded(inst.ID)
	if err != nil {
		t.Fatalf("EarliestHistoryNeeded: %v", err)
	}
	want := day(2).UTC().Format(time.DateOnly)
	if needed != want {
		t.Errorf("history needed from %q, want the first trade %q", needed, want)
	}
}

// An edit is a correction and recomputes every instalment from the new terms,
// but only the ones still waiting move: a recorded buy answers its instalment
// by instrument and date, never by amount, so the ledger is untouched.
func TestUpdatePlanCorrectsPromptsButNotWhatWasRecorded(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "saver")
	inst := seedInstrument(t, s, "2330")
	plan := seedPlan(t, s, user.ID, inst.ID, "5", 300_000, 0)
	closesFrom(t, s, inst.ID, 0, 50_000)

	before, err := s.PendingPurchases(user.ID, day(70))
	if err != nil {
		t.Fatalf("PendingPurchases: %v", err)
	}
	oldest := before[len(before)-1].DueOn
	due, _ := time.Parse(time.DateOnly, oldest)
	if _, err := s.CreateTransaction(models.Transaction{
		ID: uuid.NewString(), UserID: user.ID, InstrumentID: inst.ID,
		Side: models.SideBuy, Quantity: shares(6), Price: 50_000,
		TradedAt: due.AddDate(0, 0, 1),
	}); err != nil {
		t.Fatalf("CreateTransaction: %v", err)
	}

	updated, err := s.UpdatePlan(user.ID, plan.ID, PlanChange{
		DaysOfMonth: "5", Amount: 500_000, StartedOn: plan.StartedOn,
	})
	if err != nil {
		t.Fatalf("UpdatePlan: %v", err)
	}
	if updated.Amount != 500_000 || updated.InstrumentID != inst.ID {
		t.Errorf("updated plan %+v, want amount 500000 on the same instrument", updated)
	}

	after, err := s.PendingPurchases(user.ID, day(70))
	if err != nil {
		t.Fatalf("PendingPurchases: %v", err)
	}
	if len(after) != len(before)-1 {
		t.Fatalf("got %d pending after the edit, want %d — the recorded one must stay answered",
			len(after), len(before)-1)
	}
	for _, p := range after {
		if p.DueOn == oldest {
			t.Errorf("the recorded instalment %s came back as pending", oldest)
		}
		if p.Amount != 500_000 {
			t.Errorf("instalment %s still asks for %d, want the corrected 500000", p.DueOn, p.Amount)
		}
	}
}

// A stopped plan cannot be moved to start after it stopped: that is a plan
// that never ran, and nothing would say so.
func TestUpdatePlanRefusesAStartAfterItsEnd(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "saver")
	inst := seedInstrument(t, s, "2330")
	plan := seedPlan(t, s, user.ID, inst.ID, "5", 300_000, 0)
	if err := s.EndPlan(user.ID, plan.ID, day(30).UTC().Format(time.DateOnly)); err != nil {
		t.Fatalf("EndPlan: %v", err)
	}

	_, err := s.UpdatePlan(user.ID, plan.ID, PlanChange{
		DaysOfMonth: "5", Amount: 300_000, StartedOn: day(31).UTC().Format(time.DateOnly),
	})
	if !errors.Is(err, ErrPlanStartsAfterEnd) {
		t.Fatalf("got %v, want ErrPlanStartsAfterEnd", err)
	}
	// Starting on the day it stopped is a plan that ran once, and is allowed.
	if _, err := s.UpdatePlan(user.ID, plan.ID, PlanChange{
		DaysOfMonth: "5", Amount: 300_000, StartedOn: day(30).UTC().Format(time.DateOnly),
	}); err != nil {
		t.Errorf("starting on the stop date: %v", err)
	}
}

// Another book's plan is not found rather than editable by id alone.
func TestUpdatePlanIsScopedToItsOwner(t *testing.T) {
	s := newTestDB(t)
	mine := seedUser(t, s, "mine")
	theirs := seedUser(t, s, "theirs")
	inst := seedInstrument(t, s, "2330")
	plan := seedPlan(t, s, theirs.ID, inst.ID, "5", 300_000, 0)

	_, err := s.UpdatePlan(mine.ID, plan.ID, PlanChange{
		DaysOfMonth: "5", Amount: 1, StartedOn: plan.StartedOn,
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
	plans, err := s.ListPlans(theirs.ID)
	if err != nil {
		t.Fatalf("ListPlans: %v", err)
	}
	if plans[0].Amount != 300_000 {
		t.Errorf("another user's edit changed the amount to %d", plans[0].Amount)
	}
}
