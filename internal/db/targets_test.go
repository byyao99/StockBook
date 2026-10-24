package db

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"stockbook/internal/models"
)

func TestSaveTargetsRoundTripsPerCurrency(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "planner")
	voo := seedInstrumentIn(t, s, "VOO", models.CurrencyUSD)
	pltr := seedInstrumentIn(t, s, "PLTR", models.CurrencyUSD)
	tsmc := seedInstrumentIn(t, s, "2330", models.CurrencyTWD)

	saved, err := s.SaveTargets(user.ID, []TargetInput{
		{InstrumentID: pltr.ID, WeightBps: 2000},
		{InstrumentID: voo.ID, WeightBps: 8000},
		{InstrumentID: tsmc.ID, WeightBps: 10000},
	})
	if err != nil {
		t.Fatalf("SaveTargets: %v", err)
	}
	// Grouped by currency, largest first within it.
	want := []string{"2330 TWD 10000", "VOO USD 8000", "PLTR USD 2000"}
	if len(saved) != len(want) {
		t.Fatalf("got %d targets, want %d", len(saved), len(want))
	}
	for i, v := range saved {
		got := v.Symbol + " " + string(v.Currency) + " " + strconv.FormatInt(v.WeightBps, 10)
		if got != want[i] {
			t.Errorf("target %d = %q, want %q", i, got, want[i])
		}
	}
}

// A planner reading 95% would have to guess where the other 5% was meant to go.
func TestSaveTargetsRefusesACurrencyNotAddingUpTo100(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "planner")
	voo := seedInstrumentIn(t, s, "VOO", models.CurrencyUSD)
	pltr := seedInstrumentIn(t, s, "PLTR", models.CurrencyUSD)
	tsmc := seedInstrumentIn(t, s, "2330", models.CurrencyTWD)

	// USD is complete; TWD is not, and the message must name TWD.
	_, err := s.SaveTargets(user.ID, []TargetInput{
		{InstrumentID: voo.ID, WeightBps: 6000},
		{InstrumentID: pltr.ID, WeightBps: 4000},
		{InstrumentID: tsmc.ID, WeightBps: 9500},
	})
	if !errors.Is(err, ErrInvalidTargets) || !strings.Contains(err.Error(), "TWD targets add up to 95.00%") {
		t.Fatalf("err = %v, want the TWD sum refused", err)
	}
}

func TestSaveTargetsRefusesBadInput(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "planner")
	voo := seedInstrumentIn(t, s, "VOO", models.CurrencyUSD)

	cases := map[string][]TargetInput{
		"zero weight":     {{InstrumentID: voo.ID, WeightBps: 0}},
		"over 100%":       {{InstrumentID: voo.ID, WeightBps: 10001}},
		"named twice":     {{InstrumentID: voo.ID, WeightBps: 5000}, {InstrumentID: voo.ID, WeightBps: 5000}},
		"unknown company": {{InstrumentID: "no-such-id", WeightBps: 10000}},
	}
	for name, targets := range cases {
		if _, err := s.SaveTargets(user.ID, targets); !errors.Is(err, ErrInvalidTargets) {
			t.Errorf("%s: err = %v, want ErrInvalidTargets", name, err)
		}
	}
}

// The set is replaced whole, and a refused save leaves the previous one intact.
func TestSaveTargetsReplacesTheWholeSetAtomically(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "planner")
	voo := seedInstrumentIn(t, s, "VOO", models.CurrencyUSD)
	pltr := seedInstrumentIn(t, s, "PLTR", models.CurrencyUSD)

	if _, err := s.SaveTargets(user.ID, []TargetInput{
		{InstrumentID: voo.ID, WeightBps: 7000},
		{InstrumentID: pltr.ID, WeightBps: 3000},
	}); err != nil {
		t.Fatalf("first save: %v", err)
	}

	if _, err := s.SaveTargets(user.ID, []TargetInput{{InstrumentID: voo.ID, WeightBps: 5000}}); err == nil {
		t.Fatal("a 50% set was accepted")
	}
	if got, _ := s.Targets(user.ID); len(got) != 2 {
		t.Fatalf("after a refused save there are %d targets, want the 2 from before", len(got))
	}

	if _, err := s.SaveTargets(user.ID, []TargetInput{{InstrumentID: voo.ID, WeightBps: 10000}}); err != nil {
		t.Fatalf("replacing: %v", err)
	}
	if got, _ := s.Targets(user.ID); len(got) != 1 || got[0].Symbol != "VOO" {
		t.Fatalf("after replacing, targets = %+v, want VOO alone", got)
	}

	if _, err := s.SaveTargets(user.ID, nil); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	if got, _ := s.Targets(user.ID); len(got) != 0 {
		t.Fatalf("after clearing, %d targets remain", len(got))
	}
}

// One user's plan is invisible to another and untouched by their saves.
func TestTargetsArePersonal(t *testing.T) {
	s := newTestDB(t)
	alice := seedUser(t, s, "alice")
	bob := seedUser(t, s, "bob")
	voo := seedInstrumentIn(t, s, "VOO", models.CurrencyUSD)

	if _, err := s.SaveTargets(alice.ID, []TargetInput{{InstrumentID: voo.ID, WeightBps: 10000}}); err != nil {
		t.Fatalf("alice: %v", err)
	}
	if got, _ := s.Targets(bob.ID); len(got) != 0 {
		t.Fatalf("bob sees %d of alice's targets", len(got))
	}
	if _, err := s.SaveTargets(bob.ID, nil); err != nil {
		t.Fatalf("bob clearing: %v", err)
	}
	if got, _ := s.Targets(alice.ID); len(got) != 1 {
		t.Fatalf("bob's clear removed alice's targets: %d left", len(got))
	}
}
