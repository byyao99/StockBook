package models

import "errors"

// ErrInsufficientShares is returned when a sell would take more shares than the
// position holds. Handlers map it to HTTP 409.
var ErrInsufficientShares = errors.New("insufficient shares")

// PositionState is the running result of folding a ledger: the shares held, the
// total cost still tied up in them, and the profit or loss already banked.
//
// Quantity and CostBasis are non-negative by construction; RealizedPL is the
// only field that can go negative.
type PositionState struct {
	Quantity   int
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
		p.CostBasis += int64(t.Quantity)*t.Price + t.Fee
		return p, nil

	case SideSell:
		if t.Quantity > p.Quantity {
			return PositionState{}, ErrInsufficientShares
		}
		costRemoved := divRoundHalfUp(p.CostBasis*int64(t.Quantity), int64(p.Quantity))
		proceeds := int64(t.Quantity)*t.Price - t.Fee
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
		p.RealizedPL += int64(t.Quantity)*t.Price - t.Fee
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
	gross := int64(t.Quantity) * t.Price
	if t.Side == SideBuy {
		return gross + t.Fee
	}
	return gross - t.Fee
}

// divRoundHalfUp divides a by b rounding halves away from zero, using integer
// arithmetic only — float division would reintroduce the representation error
// that integer cents exist to avoid. b must be positive; a is non-negative in
// every call site (it is a cost basis times a share count).
//
// Overflow is not a practical concern: CostBasis would have to exceed roughly
// 9.2e18/quantity cents, i.e. tens of billions of dollars on a million-share
// position, before int64 saturates.
func divRoundHalfUp(a, b int64) int64 {
	if b == 0 {
		return 0
	}
	if a >= 0 {
		return (a + b/2) / b
	}
	return -((-a + b/2) / b)
}
