package db

import (
	"sort"
	"time"

	"stockbook/internal/models"
)

// HindsightRow is one instrument's answer to "what did selling cost me?".
//
// SellingGain is Proceeds - ValueIfHeld, so the identity to check is
// SellingGain == Proceeds - ValueIfHeld. Negative means the shares would be
// worth more today than the sales brought in; positive means selling beat
// holding on.
//
// SharesHeld is the position now, and is here to make a re-entry visible.
// Selling 100 shares and buying them back cheaper the following week shows as
// "sold 100, holds 100", which is the reader's signal that the loss this row
// reports was not actually taken — see HindsightReport for why the report does
// not try to net that out itself.
type HindsightRow struct {
	InstrumentID string          `json:"instrument_id"`
	Symbol       string          `json:"symbol"`
	Name         string          `json:"name"`
	Market       string          `json:"market"`
	Currency     models.Currency `json:"currency"`
	Sells        int             `json:"sells"`
	SharesSold   int             `json:"shares_sold"`
	SharesHeld   int             `json:"shares_held"`
	Proceeds     int64           `json:"proceeds"`
	ValueIfHeld  int64           `json:"value_if_held"`
	SellingGain  int64           `json:"selling_gain"`
	LastPrice    int64           `json:"last_price"`
}

// HindsightSummary totals one currency's sell decisions over the period, with
// the per-instrument rows behind it.
//
// UnpricedSales counts sales whose instrument has no quote. There is no price
// to compare against, so they are left out of the totals rather than valued at
// zero — which would report every one of them as a brilliant sale.
type HindsightSummary struct {
	Currency      models.Currency `json:"currency"`
	Sells         int             `json:"sells"`
	SharesSold    int             `json:"shares_sold"`
	Proceeds      int64           `json:"proceeds"`
	ValueIfHeld   int64           `json:"value_if_held"`
	SellingGain   int64           `json:"selling_gain"`
	UnpricedSales int             `json:"unpriced_sales"`
	Instruments   []HindsightRow  `json:"instruments"`
}

// hindsightScanRow is the flat scan target for the report's join.
type hindsightScanRow struct {
	InstrumentID string
	Symbol       string
	Name         string
	Market       string
	Currency     models.Currency
	Quantity     int
	NetAmount    int64
	LastPrice    *int64
}

// HindsightReport answers what userID's sales between from and to would be
// worth if the shares had never been sold. Either bound may be nil for an
// open-ended period.
//
// **The comparison is exact and needs no replay.** Refolding the ledger with the
// sells removed and valuing the result looks like the obvious implementation,
// but the difference between the two worlds collapses:
//
//	held on to everything = market value of every share ever bought - what they cost
//	what actually happened = market value of the shares still held + sale proceeds - what they cost
//	difference             = sale proceeds - (shares sold x today's price)
//
// The cost paid cancels, and so does every dividend — which matters, because
// dividends were recorded against the shares actually held, and a replay would
// have had to either under-count them or invent the ones the unsold shares would
// have paid. Neither is necessary: only the sold shares and today's quote are.
//
// **What this does not model is the cash.** Money from a sale usually buys
// something else, and this system has no cash balance to follow it into. So the
// claim here is the narrow, factual one — these shares fetched this much, and
// they would be worth this much now — and not "you would be richer today". A
// sale followed by a re-entry into the same instrument is the case where the two
// readings diverge most, which is why SharesHeld is reported alongside.
//
// Sales of an instrument with no quote have nothing to be compared against and
// are counted in UnpricedSales rather than folded in, on the same terms as every
// other unknown here. The instrument's current symbol and name are used rather
// than the ledger's snapshot, so a rename does not split one holding into two rows.
func (d *DB) HindsightReport(userID string, from, to *time.Time) ([]HindsightSummary, error) {
	q := d.db.Model(&models.Transaction{}).
		Joins("JOIN instruments ON instruments.id = transactions.instrument_id").
		Where("transactions.user_id = ? AND transactions.side = ?", userID, models.SideSell)
	if from != nil {
		q = q.Where("transactions.traded_at >= ?", *from)
	}
	if to != nil {
		q = q.Where("transactions.traded_at <= ?", *to)
	}

	sales := []hindsightScanRow{}
	if err := q.Select(`transactions.instrument_id AS instrument_id,
		transactions.quantity AS quantity,
		transactions.net_amount AS net_amount,
		instruments.symbol AS symbol,
		instruments.name AS name,
		instruments.market AS market,
		instruments.currency AS currency,
		instruments.last_price AS last_price`).Scan(&sales).Error; err != nil {
		return nil, err
	}

	type bucket struct {
		summary *HindsightSummary
		byID    map[string]*HindsightRow
	}
	buckets := map[models.Currency]*bucket{}

	for _, s := range sales {
		b, ok := buckets[s.Currency]
		if !ok {
			b = &bucket{
				summary: &HindsightSummary{Currency: s.Currency},
				byID:    map[string]*HindsightRow{},
			}
			buckets[s.Currency] = b
		}
		if s.LastPrice == nil {
			b.summary.UnpricedSales++
			continue
		}

		row, ok := b.byID[s.InstrumentID]
		if !ok {
			row = &HindsightRow{
				InstrumentID: s.InstrumentID,
				Symbol:       s.Symbol,
				Name:         s.Name,
				Market:       s.Market,
				Currency:     s.Currency,
				LastPrice:    *s.LastPrice,
			}
			b.byID[s.InstrumentID] = row
		}

		valueIfHeld := int64(s.Quantity) * *s.LastPrice
		row.Sells++
		row.SharesSold += s.Quantity
		row.Proceeds += s.NetAmount
		row.ValueIfHeld += valueIfHeld

		b.summary.Sells++
		b.summary.SharesSold += s.Quantity
		b.summary.Proceeds += s.NetAmount
		b.summary.ValueIfHeld += valueIfHeld
	}

	held, err := d.sharesHeld(userID)
	if err != nil {
		return nil, err
	}

	summaries := make([]HindsightSummary, 0, len(buckets))
	for _, b := range buckets {
		b.summary.SellingGain = b.summary.Proceeds - b.summary.ValueIfHeld

		instruments := make([]HindsightRow, 0, len(b.byID))
		for id, row := range b.byID {
			row.SellingGain = row.Proceeds - row.ValueIfHeld
			row.SharesHeld = held[id]
			instruments = append(instruments, *row)
		}
		// Worst decision first. This is a report read to learn something, and
		// what there is to learn sits at the regret end; symbol breaks ties so
		// the order does not wander between requests.
		sort.Slice(instruments, func(i, j int) bool {
			if instruments[i].SellingGain != instruments[j].SellingGain {
				return instruments[i].SellingGain < instruments[j].SellingGain
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

// sharesHeld maps instrument ID to the shares userID holds now.
func (d *DB) sharesHeld(userID string) (map[string]int, error) {
	var positions []models.Position
	if err := d.db.Where("user_id = ?", userID).Find(&positions).Error; err != nil {
		return nil, err
	}
	held := make(map[string]int, len(positions))
	for _, p := range positions {
		held[p.InstrumentID] = p.Quantity
	}
	return held, nil
}
