package db

import (
	"sort"
	"time"

	"stockbook/internal/models"
)

// Valuing a book as it stood on a past date is the one piece the equity curve
// and a windowed return both need, and it is the subtle part of either: which
// instruments can be valued at all, and what price stands on a day the market
// did not open. Both answers live here so the two reports cannot drift apart on
// them — the same reason curveMath.ts mirrors the valuation rules rather than
// inventing its own.

// priceSeries is one instrument's stored closes, indexed for lookup and ordered
// for iteration.
type priceSeries struct {
	closes map[string]int64
	dates  []string
}

// on returns the close in effect on date: the one for that session, or the most
// recent before it. The second result reports whether any price is known yet.
//
// Carrying the last close forward is what makes a holiday or a trading halt a
// non-event rather than a hole. It never carries *backwards*: a date before the
// series begins has no price, which is the case that excludes an instrument
// from a report entirely.
func (s priceSeries) on(date string) (int64, bool) {
	if close, ok := s.closes[date]; ok {
		return close, true
	}
	i := sort.SearchStrings(s.dates, date)
	if i == 0 {
		return 0, false
	}
	return s.closes[s.dates[i-1]], true
}

// pricedLedger is one currency's ledger paired with the stored closes that can
// value it, and a count of what had to be left out.
type pricedLedger struct {
	ledger []curveTx
	series map[string]priceSeries
	// sessions is every date any included instrument has a close for — the
	// finest grid the stored data supports.
	sessions map[string]bool
	// firstTraded is the day each included instrument was first bought, which is
	// how far back its prices have to reach.
	firstTraded map[string]string
	// withoutHistory counts instruments dropped whole, trades and all.
	withoutHistory int
}

// priceLedger loads the stored closes for everything in one currency's ledger,
// dropping any instrument whose history does not reach back to the day it was
// first traded.
//
// The exclusion is deliberate and total. Prices that start after the holding
// does cannot value it for the days between, and counting it short would
// understate the whole book for exactly that stretch — which looks like a real
// drawdown, and is not. A successful sync always satisfies the rule, since
// history is fetched from the first trade.
func (d *DB) priceLedger(ledger []curveTx, to string) (pricedLedger, error) {
	p := pricedLedger{
		ledger:      ledger,
		series:      map[string]priceSeries{},
		sessions:    map[string]bool{},
		firstTraded: map[string]string{},
	}

	for _, tx := range ledger {
		date := tx.TradedAt.UTC().Format(time.DateOnly)
		if seen, ok := p.firstTraded[tx.InstrumentID]; !ok || date < seen {
			p.firstTraded[tx.InstrumentID] = date
		}
	}

	for id, first := range p.firstTraded {
		rows, err := d.DailyCloseSeries(id, "0000-01-01", to)
		if err != nil {
			return p, err
		}
		if len(rows) == 0 || rows[0].Date > first {
			p.withoutHistory++
			delete(p.firstTraded, id)
			continue
		}
		s := priceSeries{
			closes: make(map[string]int64, len(rows)),
			dates:  make([]string, 0, len(rows)),
		}
		for _, r := range rows {
			s.closes[r.Date] = r.Close
			s.dates = append(s.dates, r.Date)
			p.sessions[r.Date] = true
		}
		p.series[id] = s
	}
	return p, nil
}

// instruments reports how many instruments this ledger names, before exclusions.
func (p pricedLedger) instruments() int {
	return len(p.firstTraded) + p.withoutHistory
}

// lastSessionOn reports the latest session any included instrument has a close
// for on or before date, and whether there is one at all.
//
// A value carried forward is only ever good for the day it was actually struck,
// and saying otherwise is the mistake the two quote timestamps exist to prevent
// elsewhere in this system. Asking for a period that runs past the last stored
// session — "this year", asked in September — must not date the closing figure
// in December: the same gain spread over a longer assumed period reports a
// lower annual rate, silently.
func (p pricedLedger) lastSessionOn(date string) (string, bool) {
	best, found := "", false
	for _, s := range p.series {
		i := sort.SearchStrings(s.dates, date)
		if i < len(s.dates) && s.dates[i] == date {
			return date, true
		}
		if i == 0 {
			continue
		}
		if candidate := s.dates[i-1]; candidate > best {
			best, found = candidate, true
		}
	}
	return best, found
}

// valueOn reports what the book was worth at the close of date, by folding every
// entry dated on or before it and pricing whatever is left held.
//
// An entry dated on a day the market did not open still counts: it folds into
// the value here exactly as it folds into the next session on the curve. Trades
// in an excluded instrument leave with it, so the value and the flows describe
// the same book.
func (p pricedLedger) valueOn(date string) (int64, error) {
	held := map[string]models.PositionState{}
	for _, tx := range p.ledger {
		if tx.TradedAt.UTC().Format(time.DateOnly) > date {
			break // the ledger is in order, so nothing later can apply
		}
		if _, ok := p.series[tx.InstrumentID]; !ok {
			continue
		}
		state, err := held[tx.InstrumentID].Apply(models.Transaction{
			Side: tx.Side, Quantity: tx.Quantity, Price: tx.Price, Fee: tx.Fee,
		})
		if err != nil {
			return 0, err
		}
		held[tx.InstrumentID] = state
	}

	var value int64
	for id, state := range held {
		if state.Quantity == 0 {
			continue
		}
		close, ok := p.series[id].on(date)
		if !ok {
			continue
		}
		value += models.Gross(state.Quantity, close)
	}
	return value, nil
}
