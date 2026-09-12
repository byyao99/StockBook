package models

import (
	"errors"
	"math/big"
)

// SharesScale is how many stored units make one share.
//
// Share counts are integers for the same reason money is: a float would
// reintroduce the representation error the rest of this system exists to avoid,
// and a position that drifts by 1e-15 of a share compounds through every cost
// basis and every report built on one. Fractional shares are real — a US broker
// filling a fixed-amount purchase hands you 2.79 of something — so the integer
// is scaled rather than whole.
//
// A millionth of a share is finer than any broker reports (Schwab and IBKR give
// four decimals, Robinhood six), so nothing is ever truncated on the way in.
const SharesScale int64 = 1_000_000

// ErrInsufficientShares is returned when a sell would take more shares than the
// position holds. Handlers map it to HTTP 409.
var ErrInsufficientShares = errors.New("insufficient shares")

// PositionState is the running result of folding a ledger: the shares held, the
// total cost still tied up in them, and the profit or loss already banked.
//
// Quantity and CostBasis are non-negative by construction; RealizedPL is the
// only field that can go negative.
type PositionState struct {
	Quantity   int64 // shares in SharesScale units, NOT whole shares
	CostBasis  int64 // total remaining cost in cents, NOT a per-share average
	RealizedPL int64 // cents, may be negative
}

// Apply folds one transaction into the state under moving-average cost.
//
// A buy adds shares and adds its cost, fees included:
//
//	quantity  += q
//	costBasis += q*price + fee
//
// A sell releases cost in proportion to the shares leaving, and banks the
// difference between the proceeds and that released cost:
//
//	costRemoved = round(costBasis * q / quantity)
//	proceeds    = q*price - fee
//	realizedPL += proceeds - costRemoved
//	costBasis  -= costRemoved
//	quantity   -= q
//
// Selling the entire position needs no rounding at all — costBasis*q/quantity is
// exactly costBasis when q == quantity — so a full exit always leaves CostBasis
// at exactly 0 with no residue. TestApplyFullExitLeavesNoResidue pins this down.
//
// A cash dividend banks its payout and moves nothing else:
//
//	realizedPL += q*price - fee
//
// Apply is pure: it returns a new state and never mutates the receiver.
func (p PositionState) Apply(t Transaction) (PositionState, error) {
	switch t.Side {
	case SideBuy:
		p.Quantity += t.Quantity
		p.CostBasis += Gross(t.Quantity, t.Price) + t.Fee
		return p, nil

	case SideSell:
		if t.Quantity > p.Quantity {
			return PositionState{}, ErrInsufficientShares
		}
		costRemoved := mulDivRoundHalfUp(p.CostBasis, t.Quantity, p.Quantity)
		proceeds := Gross(t.Quantity, t.Price) - t.Fee
		p.RealizedPL += proceeds - costRemoved
		p.CostBasis -= costRemoved
		p.Quantity -= t.Quantity
		return p, nil

	case SideDividend:
		// Income, banked whole: quantity and cost basis are untouched, so a
		// payout cannot flatter the unrealized gain on shares still held.
		//
		// Deliberately not checked against the shares on hand. A dividend is
		// earned on the ex-dividend date but lands weeks later, by which time
		// the shares may have been sold, and refusing that entry would decline
		// to record money that genuinely arrived. Nothing here runs out or goes
		// negative, so there is no invariant needing the guard a sell requires.
		p.RealizedPL += Gross(t.Quantity, t.Price) - t.Fee
		return p, nil

	default:
		return PositionState{}, errors.New("unknown transaction side: " + string(t.Side))
	}
}

// NetAmount returns the cash movement a transaction represents, in cents: what a
// buy costs (price plus fee) or what a sell or dividend yields (price minus
// fee). It is the server's own arithmetic — client-supplied amounts are never
// trusted.
func NetAmount(t Transaction) int64 {
	gross := Gross(t.Quantity, t.Price)
	if t.Side == SideBuy {
		return gross + t.Fee
	}
	return gross - t.Fee
}

// Gross is what a quantity at a price comes to, in cents.
//
// This is where fractional shares put a rounding step that whole ones never
// needed: 2.79 shares at $10.01 is $27.9279, which is not a whole cent and
// never will be. The rounding happens **once, here**, and what is stored
// afterwards is the exact integer result — the same discipline the fee estimate
// follows when it multiplies a rate by an amount. Rounding anywhere else, or
// more than once, is what makes a cost basis drift.
func Gross(quantity, price int64) int64 {
	return mulDivRoundHalfUp(price, quantity, SharesScale)
}

// mulDivRoundHalfUp returns a*b/c, rounding halves away from zero, exactly.
//
// The product is computed in arbitrary precision because with scaled share
// counts it no longer reliably fits in an int64: a cost basis in cents times a
// quantity in millionths passes 9.2e18 at portfolio sizes that are large but
// not absurd, and the failure mode is silent — a wrapped product yields a cost
// basis that is simply wrong, with nothing to indicate it. The old comment here
// said overflow was not a practical concern, which was true while quantities
// were whole shares and stopped being true the moment they were scaled.
//
// This runs once per ledger entry on a replay, so the allocation is irrelevant
// beside being unconditionally right. Only the division rounds; the
// multiplication is exact.
func mulDivRoundHalfUp(a, b, c int64) int64 {
	if c == 0 {
		return 0
	}
	product := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	divisor := big.NewInt(c)

	// Round half away from zero: add half the divisor, with the sign of the
	// product, before truncating toward zero.
	half := new(big.Int).Rsh(new(big.Int).Abs(divisor), 1)
	if product.Sign() < 0 {
		half.Neg(half)
	}
	product.Add(product, half)

	return new(big.Int).Quo(product, divisor).Int64()
}
