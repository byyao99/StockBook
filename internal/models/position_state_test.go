package models

import (
	"errors"
	"testing"
)

// buy and sell build transactions for the fold tests. Prices and fees are cents.
func buy(qty int, price, fee int64) Transaction {
	return Transaction{Side: SideBuy, Quantity: qty, Price: price, Fee: fee}
}

func sell(qty int, price, fee int64) Transaction {
	return Transaction{Side: SideSell, Quantity: qty, Price: price, Fee: fee}
}

// dividend pays perShare on qty shares, less any withholding in fee.
func dividend(qty int, perShare, fee int64) Transaction {
	return Transaction{Side: SideDividend, Quantity: qty, Price: perShare, Fee: fee}
}

// fold applies a whole sequence, failing the test on the first error.
func fold(t *testing.T, txs ...Transaction) PositionState {
	t.Helper()
	var state PositionState
	for i, tx := range txs {
		next, err := state.Apply(tx)
		if err != nil {
			t.Fatalf("Apply(%d): %v", i, err)
		}
		state = next
	}
	return state
}

func TestApplyMovingAverage(t *testing.T) {
	tests := []struct {
		name string
		txs  []Transaction
		want PositionState
	}{
		{
			name: "single buy",
			txs:  []Transaction{buy(100, 5000, 0)},
			want: PositionState{Quantity: 100, CostBasis: 500000},
		},
		{
			name: "two buys average the cost",
			txs:  []Transaction{buy(100, 5000, 0), buy(100, 7000, 0)},
			want: PositionState{Quantity: 200, CostBasis: 1200000},
		},
		{
			// Sell half of a 200-share position averaging 60.00: half the cost
			// (600000) is released, proceeds are 800000, so 200000 is banked.
			name: "partial sell releases cost proportionally",
			txs:  []Transaction{buy(100, 5000, 0), buy(100, 7000, 0), sell(100, 8000, 0)},
			want: PositionState{Quantity: 100, CostBasis: 600000, RealizedPL: 200000},
		},
		{
			name: "buy fees join the cost basis",
			txs:  []Transaction{buy(100, 5000, 1425)},
			want: PositionState{Quantity: 100, CostBasis: 501425},
		},
		{
			// Sell fees (brokerage plus transaction tax) come off the proceeds,
			// so they reduce the realized gain rather than the remaining cost.
			name: "sell fees reduce realized profit",
			txs:  []Transaction{buy(100, 5000, 0), sell(100, 6000, 2000)},
			want: PositionState{Quantity: 0, CostBasis: 0, RealizedPL: 98000},
		},
		{
			name: "a loss is realized as a negative",
			txs:  []Transaction{buy(100, 5000, 0), sell(100, 4000, 0)},
			want: PositionState{Quantity: 0, CostBasis: 0, RealizedPL: -100000},
		},
		{
			// Re-buying after a full exit starts a fresh cost basis but keeps the
			// realized total — realized P/L is cumulative over the account's life.
			name: "re-entry keeps realized but resets cost",
			txs:  []Transaction{buy(10, 1000, 0), sell(10, 1500, 0), buy(5, 2000, 0)},
			want: PositionState{Quantity: 5, CostBasis: 10000, RealizedPL: 5000},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := fold(t, tc.txs...); got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// A full exit must leave no cost behind. Selling everything makes the
// proportional release exact (costBasis*q/q == costBasis), so any residue here
// would mean the rounding in Apply is wrong — this is the most sensitive check
// that the proportional math is right.
func TestApplyFullExitLeavesNoResidue(t *testing.T) {
	// Deliberately unfriendly numbers: 3 shares at 10.00 plus 1 at 10.01 gives a
	// total of 4001 cents over 4 shares, i.e. 1000.25 cents per share, which is
	// not representable as an integer average.
	state := fold(t, buy(3, 1000, 0), buy(1, 1001, 0))
	if state.CostBasis != 4001 {
		t.Fatalf("cost basis before exit = %d, want 4001", state.CostBasis)
	}

	state = fold(t, buy(3, 1000, 0), buy(1, 1001, 0), sell(4, 1200, 0))
	if state.Quantity != 0 {
		t.Errorf("quantity = %d, want 0", state.Quantity)
	}
	if state.CostBasis != 0 {
		t.Errorf("cost basis = %d, want exactly 0 (rounding residue leaked)", state.CostBasis)
	}
	if want := int64(4*1200 - 4001); state.RealizedPL != want {
		t.Errorf("realized = %d, want %d", state.RealizedPL, want)
	}
}

// A partial sell out of an unevenly-priced position must round the released cost
// half-up, and the released cost plus the remaining cost must still equal the
// original total — no cents may be created or destroyed.
func TestApplyPartialSellRoundsHalfUp(t *testing.T) {
	before := fold(t, buy(3, 1000, 0), buy(1, 1001, 0)) // 4 shares, 4001 cents
	after := fold(t, buy(3, 1000, 0), buy(1, 1001, 0), sell(1, 1500, 0))

	// 4001 * 1 / 4 = 1000.25 -> 1000 half-up.
	const wantRemoved = 1000
	if got := before.CostBasis - after.CostBasis; got != wantRemoved {
		t.Errorf("cost removed = %d, want %d", got, wantRemoved)
	}
	if after.CostBasis != 3001 {
		t.Errorf("remaining cost = %d, want 3001", after.CostBasis)
	}
	if want := int64(1500 - wantRemoved); after.RealizedPL != want {
		t.Errorf("realized = %d, want %d", after.RealizedPL, want)
	}

	// Selling the remaining 3 shares must still zero out the basis exactly.
	final, err := after.Apply(sell(3, 1500, 0))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if final.CostBasis != 0 || final.Quantity != 0 {
		t.Errorf("after full exit: quantity=%d cost=%d, want 0/0", final.Quantity, final.CostBasis)
	}
}

// A cash dividend is income, not a refund of what the shares cost. Banking it
// against the cost basis instead would leave the shares looking cheaper than
// they were and quietly inflate the unrealized gain still showing on them.
func TestApplyDividendIsIncomeNotCostReduction(t *testing.T) {
	before := fold(t, buy(1000, 70000, 0))
	after := fold(t, buy(1000, 70000, 0), dividend(1000, 500, 0))

	if after.CostBasis != before.CostBasis {
		t.Errorf("cost basis moved from %d to %d; a dividend must not touch it",
			before.CostBasis, after.CostBasis)
	}
	if after.Quantity != before.Quantity {
		t.Errorf("quantity moved from %d to %d", before.Quantity, after.Quantity)
	}
	if want := int64(1000 * 500); after.RealizedPL != want {
		t.Errorf("realized = %d, want %d", after.RealizedPL, want)
	}
}

// Withholding comes off the payout, exactly as a selling fee comes off proceeds.
func TestApplyDividendWithholdingReducesIncome(t *testing.T) {
	state := fold(t, buy(1000, 70000, 0), dividend(1000, 500, 10600))
	if want := int64(1000*500 - 10600); state.RealizedPL != want {
		t.Errorf("realized = %d, want %d", state.RealizedPL, want)
	}
}

// A payout is earned on the ex-dividend date but arrives weeks later, by which
// time the shares may be gone. Recording it must not require holding them —
// unlike a sell, nothing here can run out.
func TestApplyDividendAfterFullExit(t *testing.T) {
	state := fold(t, buy(1000, 70000, 0), sell(1000, 90000, 0))
	after, err := state.Apply(dividend(1000, 500, 0))
	if err != nil {
		t.Fatalf("dividend on sold shares: %v", err)
	}
	if after.Quantity != 0 || after.CostBasis != 0 {
		t.Errorf("closed position moved: %+v", after)
	}
	if want := state.RealizedPL + 1000*500; after.RealizedPL != want {
		t.Errorf("realized = %d, want %d", after.RealizedPL, want)
	}
}

func TestApplyRejectsOversell(t *testing.T) {
	state := fold(t, buy(10, 1000, 0))

	if _, err := state.Apply(sell(11, 1200, 0)); !errors.Is(err, ErrInsufficientShares) {
		t.Errorf("selling 11 of 10: got %v, want ErrInsufficientShares", err)
	}
	// Selling from nothing is the same error, not a panic or a negative holding.
	if _, err := (PositionState{}).Apply(sell(1, 1200, 0)); !errors.Is(err, ErrInsufficientShares) {
		t.Errorf("selling from empty: got %v, want ErrInsufficientShares", err)
	}
	// Selling exactly what is held is fine.
	if _, err := state.Apply(sell(10, 1200, 0)); err != nil {
		t.Errorf("selling all 10: unexpected error %v", err)
	}
}

func TestApplyIsPure(t *testing.T) {
	original := PositionState{Quantity: 10, CostBasis: 10000}
	if _, err := original.Apply(sell(5, 2000, 0)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if (original != PositionState{Quantity: 10, CostBasis: 10000}) {
		t.Errorf("receiver was mutated: %+v", original)
	}
}

func TestNetAmount(t *testing.T) {
	// A buy costs the gross plus the fee; a sell yields the gross minus the fee.
	if got := NetAmount(buy(100, 5000, 1425)); got != 501425 {
		t.Errorf("buy net = %d, want 501425", got)
	}
	if got := NetAmount(sell(100, 5000, 1425)); got != 498575 {
		t.Errorf("sell net = %d, want 498575", got)
	}
	// A dividend is cash in, like a sell: the withholding comes off it.
	if got := NetAmount(dividend(1000, 500, 10600)); got != 489400 {
		t.Errorf("dividend net = %d, want 489400", got)
	}
}

func TestSideValid(t *testing.T) {
	for _, s := range Sides {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	if TransactionSide("split").Valid() {
		t.Error("an unknown side should not be valid")
	}
	// Only entries that bank something carry a realized stamp.
	if SideBuy.Realizes() {
		t.Error("a buy realizes nothing")
	}
	if !SideSell.Realizes() || !SideDividend.Realizes() {
		t.Error("sells and dividends both realize a result")
	}
}

func TestDivRoundHalfUp(t *testing.T) {
	tests := []struct{ a, b, want int64 }{
		{10, 4, 3},      // 2.5 rounds away from zero
		{14, 4, 4},      // 3.5 rounds away from zero
		{4001, 4, 1000}, // 1000.25 rounds down
		{4003, 4, 1001}, // 1000.75 rounds up
		{100, 10, 10},   // exact
		{0, 7, 0},
		{5, 0, 0}, // guard: division by zero yields zero rather than panicking
	}
	for _, tc := range tests {
		if got := divRoundHalfUp(tc.a, tc.b); got != tc.want {
			t.Errorf("divRoundHalfUp(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestRoleValid(t *testing.T) {
	for _, r := range []Role{RoleUser, RoleAdmin} {
		if !r.Valid() {
			t.Errorf("%q should be valid", r)
		}
	}
	for _, r := range []Role{"", "staff", "root", "USER"} {
		if r.Valid() {
			t.Errorf("%q should be invalid", r)
		}
	}
}

func TestTransactionSideValid(t *testing.T) {
	for _, s := range []TransactionSide{SideBuy, SideSell} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []TransactionSide{"", "BUY", "short"} {
		if s.Valid() {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func TestCanonicalMarket(t *testing.T) {
	if got, ok := CanonicalMarket("twse"); !ok || got != "TWSE" {
		t.Errorf(`CanonicalMarket("twse") = %q, %v; want "TWSE", true`, got, ok)
	}
	if _, ok := CanonicalMarket("LSE"); ok {
		t.Error(`CanonicalMarket("LSE") should not match`)
	}
}
