package db

import (
	"sort"
	"time"

	"stockbook/internal/models"
)

// RealizedRow is one instrument's realized result over a reporting period.
//
// The two sources of a realized result are kept apart. TradingPL is what the
// sales banked and Dividends is what was paid out for holding the shares; they
// answer different questions, are taxed differently in most places, and a single
// figure would hide a book that lost money on price but made it back on income.
// RealizedPL is their sum, the one number for "how did this go".
//
// Proceeds and Cost describe the sales alone, so TradingPL == Proceeds - Cost
// always holds. Cost is reported rather than left to be inferred because it is
// what a tax form asks for next to the proceeds, and it is not recoverable from
// the ledger alone — it comes from the moving average at the moment of each sale.
type RealizedRow struct {
	InstrumentID  string          `json:"instrument_id"`
	Symbol        string          `json:"symbol"`
	Name          string          `json:"name"`
	Market        string          `json:"market"`
	Currency      models.Currency `json:"currency"`
	RealizedPL    int64           `json:"realized_pl"`
	TradingPL     int64           `json:"trading_pl"`
	Dividends     int64           `json:"dividends"`
	Proceeds      int64           `json:"proceeds"`
	Cost          int64           `json:"cost"`
	Sells         int             `json:"sells"`
	DividendCount int             `json:"dividend_count"`
}

// RealizedSummary totals one currency's realized result over the period, with
// the per-instrument rows behind it.
//
// Like every other total in this system it is per currency: there is no FX rate
// here, so a TWD gain and a USD gain are never added into one number.
//
// UnstampedEntries counts sales and dividends whose result is not known — rows
// the startup backfill could not replay. They are left out of the totals rather
// than folded in as zero, and reported so a caller can say the period is not
// fully accounted for instead of presenting a short total as complete. It is
// normally 0.
type RealizedSummary struct {
	Currency         models.Currency `json:"currency"`
	RealizedPL       int64           `json:"realized_pl"`
	TradingPL        int64           `json:"trading_pl"`
	Dividends        int64           `json:"dividends"`
	Proceeds         int64           `json:"proceeds"`
	Cost             int64           `json:"cost"`
	Sells            int             `json:"sells"`
	DividendCount    int             `json:"dividend_count"`
	UnstampedEntries int             `json:"unstamped_entries"`
	Instruments      []RealizedRow   `json:"instruments"`
}

// realizedRow is the flat scan target for the report's join.
type realizedRow struct {
	InstrumentID string
	Symbol       string
	Name         string
	Market       string
	Currency     models.Currency
	Side         models.TransactionSide
	NetAmount    int64
	RealizedPL   *int64
}

// RealizedReport summarizes what userID banked between from and to, one entry
// per currency, ordered by currency so the output is stable. Either bound may be
// nil for an open-ended period.
//
// Buys are excluded: money moved into a cost basis has not been realized. The
// instrument's current symbol and name are used rather than the ledger's
// snapshot, because the report groups by instrument and a rename would otherwise
// split one holding into two rows.
func (d *DB) RealizedReport(userID string, from, to *time.Time) ([]RealizedSummary, error) {
	q := d.db.Model(&models.Transaction{}).
		Joins("JOIN instruments ON instruments.id = transactions.instrument_id").
		Where("transactions.user_id = ? AND transactions.side <> ?", userID, models.SideBuy)
	if from != nil {
		q = q.Where("transactions.traded_at >= ?", *from)
	}
	if to != nil {
		q = q.Where("transactions.traded_at <= ?", *to)
	}

	rows := []realizedRow{}
	if err := q.Select(`transactions.instrument_id AS instrument_id,
		transactions.side AS side,
		transactions.net_amount AS net_amount,
		transactions.realized_pl AS realized_pl,
		instruments.symbol AS symbol,
		instruments.name AS name,
		instruments.market AS market,
		instruments.currency AS currency`).Scan(&rows).Error; err != nil {
		return nil, err
	}

	type bucket struct {
		summary *RealizedSummary
		byID    map[string]*RealizedRow
	}
	buckets := map[models.Currency]*bucket{}

	for _, r := range rows {
		b, ok := buckets[r.Currency]
		if !ok {
			b = &bucket{
				summary: &RealizedSummary{Currency: r.Currency},
				byID:    map[string]*RealizedRow{},
			}
			buckets[r.Currency] = b
		}
		if r.RealizedPL == nil {
			b.summary.UnstampedEntries++
			continue
		}

		row, ok := b.byID[r.InstrumentID]
		if !ok {
			row = &RealizedRow{
				InstrumentID: r.InstrumentID,
				Symbol:       r.Symbol,
				Name:         r.Name,
				Market:       r.Market,
				Currency:     r.Currency,
			}
			b.byID[r.InstrumentID] = row
		}

		row.RealizedPL += *r.RealizedPL
		b.summary.RealizedPL += *r.RealizedPL

		if r.Side == models.SideDividend {
			row.Dividends += *r.RealizedPL
			row.DividendCount++
			b.summary.Dividends += *r.RealizedPL
			b.summary.DividendCount++
			continue
		}

		// A sale's proceeds are its own net amount; the cost it released is what
		// is left once the realized result is taken out of them.
		row.TradingPL += *r.RealizedPL
		row.Proceeds += r.NetAmount
		row.Cost += r.NetAmount - *r.RealizedPL
		row.Sells++

		b.summary.TradingPL += *r.RealizedPL
		b.summary.Proceeds += r.NetAmount
		b.summary.Cost += r.NetAmount - *r.RealizedPL
		b.summary.Sells++
	}

	summaries := make([]RealizedSummary, 0, len(buckets))
	for _, b := range buckets {
		instruments := make([]RealizedRow, 0, len(b.byID))
		for _, row := range b.byID {
			instruments = append(instruments, *row)
		}
		// Biggest contribution first, which is what a reader looks for; symbol
		// breaks ties so the order does not wander between requests.
		sort.Slice(instruments, func(i, j int) bool {
			if instruments[i].RealizedPL != instruments[j].RealizedPL {
				return instruments[i].RealizedPL > instruments[j].RealizedPL
			}
			return instruments[i].Symbol < instruments[j].Symbol
		})
		b.summary.Instruments = instruments
		summaries = append(summaries, *b.summary)
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Currency < summaries[j].Currency
	})
	return summaries, nil
}
