package db

import (
	"math"
	"sort"
	"time"

	"stockbook/internal/models"
)

// indexBase is where every performance index starts: 10000 minor units, read as
// 100.00. Expressing the curve as the growth of a notional 100 is what makes two
// books — or a book and an index — comparable on one axis when the money in them
// is nothing alike.
const indexBase = 10000

// CurvePoint is one trading day of a book's history.
//
// MarketValue is what the holdings were worth at that close and NetInvested is
// what had been put in by then, both of which a reader wants to see. Neither is
// a return: a book that doubles its contributions doubles its market value
// without having earned anything. Index is the figure that answers performance,
// because it is built from daily returns with the contributions divided out.
type CurvePoint struct {
	Date        string `json:"date"`
	MarketValue int64  `json:"market_value"`
	NetInvested int64  `json:"net_invested"`
	Index       int64  `json:"index"`
}

// CurrencyCurve is one currency's daily history, with the figures derived from
// it. One per currency, like every other total here.
//
// TWRBps is the time-weighted return over the window: what a single unit of
// money left alone in this book from the first day to the last would have
// earned. It is deliberately not the same question as the money-weighted return
// (`/reports/returns`), and the pair is the standard one — time-weighted
// measures the picking, money-weighted measures the picking *and* the timing of
// what was put in.
//
// MaxDrawdownBps is the deepest peak-to-trough fall of the index, reported as a
// positive number: 2500 means the book was once 25% below its high-water mark.
// It is measured on the index and never on MarketValue, because a withdrawal
// would otherwise register as a crash.
//
// WithoutHistory counts instruments left out of the curve entirely for want of
// stored prices covering the period they were held. Their trades are excluded
// along with their value, on the same terms as the returns report — a holding
// that cannot be valued must not be valued at zero.
type CurrencyCurve struct {
	Currency       models.Currency `json:"currency"`
	Points         []CurvePoint    `json:"points"`
	TWRBps         *int64          `json:"twr_bps"`
	AnnualizedBps  *int64          `json:"annualized_bps"`
	MaxDrawdownBps *int64          `json:"max_drawdown_bps"`
	Instruments    int             `json:"instruments"`
	WithoutHistory int             `json:"without_history"`
	// Unavailable explains an empty curve in words, and is empty when there are
	// points. A book with no stored history reads as "sync prices", not as a
	// flat line at zero.
	Unavailable string `json:"unavailable,omitempty"`
}

// curveTx is the flat scan target for the ledger side of a curve.
type curveTx struct {
	InstrumentID string
	Currency     models.Currency
	Side         models.TransactionSide
	Quantity     int
	Price        int64
	Fee          int64
	NetAmount    int64
	TradedAt     time.Time
}

// EquityCurve builds the daily history of userID's book between from and to,
// one entry per currency. Both bounds are YYYY-MM-DD; either may be empty, which
// opens that end.
//
// The index is chained from daily returns, each one measuring only what the
// market did to what was already held:
//
//	r     = (value at close - money paid in today) / value at the previous close - 1
//	index = index yesterday * (1 + r)
//
// Taking the day's flows out of the *numerator* is what removes contributions
// from the result. A purchase adds its cost to the closing value and has that
// same cost subtracted straight back off, so buying more moves the index only by
// however the new shares then perform — never by the size of the purchase.
// Without that, saving harder would look like skill.
//
// Flows are treated as arriving at the end of the day, and the alternative —
// counting them into the denominator as if they had been there since the open —
// is wrong for the data this system has. The only price stored for a session is
// its close, so a purchase is most nearly a purchase *at* the close: money that
// has not been at work yet. Putting it in the denominator would divide the day's
// real gain by a base that money never earned on, and a large deposit would
// silently drag the day's return toward zero.
//
// The first session with anything in it anchors the index at 100 and reports no
// return, because there is no previous close to measure one against. A trade
// dated on a day the market did not open folds into the next session rather than
// being dropped.
//
// One artifact is left, and it is the standard one: a trade filled away from the
// close credits that difference to the day it happened, since the flow subtracted
// is the cash that actually moved while the shares are valued at the close.
// Pricing the flow at the close instead would hide a genuinely good fill; the
// money-weighted return reports it too, and more directly.
//
// An instrument whose stored history does not reach back to the day it was first
// traded is excluded whole, trades and all, and counted in WithoutHistory. The
// alternative is a curve that silently understates the book for every day before
// its prices begin, which looks like a real drawdown and is not.
func (d *DB) EquityCurve(userID, from, to string) ([]CurrencyCurve, error) {
	txs := []curveTx{}
	err := d.db.Model(&models.Transaction{}).
		Joins("JOIN instruments ON instruments.id = transactions.instrument_id").
		Where("transactions.user_id = ?", userID).
		Select(`transactions.instrument_id AS instrument_id,
			transactions.side AS side,
			transactions.quantity AS quantity,
			transactions.price AS price,
			transactions.fee AS fee,
			transactions.net_amount AS net_amount,
			transactions.traded_at AS traded_at,
			instruments.currency AS currency`).
		Order(qualifiedLedgerOrder).Scan(&txs).Error
	if err != nil {
		return nil, err
	}
	if len(txs) == 0 {
		return []CurrencyCurve{}, nil
	}

	if to == "" {
		to = time.Now().UTC().Format(time.DateOnly)
	}

	// Group the ledger by currency: two currencies are two separate books, and
	// there is no rate here to merge them with.
	byCurrency := map[models.Currency][]curveTx{}
	for _, tx := range txs {
		byCurrency[tx.Currency] = append(byCurrency[tx.Currency], tx)
	}

	curves := make([]CurrencyCurve, 0, len(byCurrency))
	for currency, ledger := range byCurrency {
		curve, err := d.currencyCurve(currency, ledger, from, to)
		if err != nil {
			return nil, err
		}
		curves = append(curves, curve)
	}
	sort.Slice(curves, func(i, j int) bool {
		return curves[i].Currency < curves[j].Currency
	})
	return curves, nil
}

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
// non-event rather than a hole in the curve. It never carries *backwards*: a
// date before the series begins has no price, which is the case that excludes an
// instrument from the curve entirely.
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

// currencyCurve builds one currency's curve from its slice of the ledger.
func (d *DB) currencyCurve(currency models.Currency, ledger []curveTx, from, to string) (CurrencyCurve, error) {
	curve := CurrencyCurve{Currency: currency, Points: []CurvePoint{}}

	// The day each instrument was first traded decides how far back its prices
	// have to reach for it to be usable.
	firstTraded := map[string]string{}
	for _, tx := range ledger {
		date := tx.TradedAt.UTC().Format(time.DateOnly)
		if seen, ok := firstTraded[tx.InstrumentID]; !ok || date < seen {
			firstTraded[tx.InstrumentID] = date
		}
	}
	curve.Instruments = len(firstTraded)

	series := map[string]priceSeries{}
	sessions := map[string]bool{}
	for id, first := range firstTraded {
		rows, err := d.DailyCloseSeries(id, "0000-01-01", to)
		if err != nil {
			return curve, err
		}
		// Prices that do not reach the first trade cannot value the holding for
		// the days between, so the instrument is left out rather than counted
		// short. A successful sync always satisfies this.
		if len(rows) == 0 || rows[0].Date > first {
			curve.WithoutHistory++
			delete(firstTraded, id)
			continue
		}
		s := priceSeries{closes: make(map[string]int64, len(rows)), dates: make([]string, 0, len(rows))}
		for _, r := range rows {
			s.closes[r.Date] = r.Close
			s.dates = append(s.dates, r.Date)
			sessions[r.Date] = true
		}
		series[id] = s
	}

	if len(series) == 0 {
		curve.Unavailable = "no holding in this currency has stored price history covering when it was held; sync prices first"
		return curve, nil
	}

	// The axis is every session any included instrument traded on, which is the
	// finest grid the stored data supports.
	axis := make([]string, 0, len(sessions))
	start := earliest(firstTraded)
	if from > start {
		start = from
	}
	for date := range sessions {
		if date >= start && date <= to {
			axis = append(axis, date)
		}
	}
	sort.Strings(axis)
	if len(axis) == 0 {
		curve.Unavailable = "no trading sessions fall in this period"
		return curve, nil
	}

	held := map[string]models.PositionState{}
	var (
		next        int // the next ledger entry not yet folded in
		netInvested int64
		prevValue   int64
		index       = float64(indexBase)
		started     bool
	)

	for _, date := range axis {
		// Everything traded on or before this session, including entries dated
		// on days the market did not open, which fold into the next one.
		var flow int64
		for next < len(ledger) && ledger[next].TradedAt.UTC().Format(time.DateOnly) <= date {
			tx := ledger[next]
			next++
			if _, ok := series[tx.InstrumentID]; !ok {
				continue // excluded instrument: its trades leave with it
			}
			state, err := held[tx.InstrumentID].Apply(models.Transaction{
				Side: tx.Side, Quantity: tx.Quantity, Price: tx.Price, Fee: tx.Fee,
			})
			if err != nil {
				return curve, err
			}
			held[tx.InstrumentID] = state

			if tx.Side == models.SideBuy {
				flow += tx.NetAmount
			} else {
				flow -= tx.NetAmount
			}
		}
		netInvested += flow

		var value int64
		for id, state := range held {
			if state.Quantity == 0 {
				continue
			}
			close, ok := series[id].on(date)
			if !ok {
				continue
			}
			value += int64(state.Quantity) * close
		}

		switch {
		case !started:
			// Nothing has been held yet, so there is no return to report and
			// nothing to plot. The first session that does hold something
			// anchors the index at its base.
			if value == 0 && flow == 0 {
				continue
			}
			started = true
		case prevValue > 0:
			index *= float64(value-flow) / float64(prevValue)
		default:
			// The book was empty at the previous close — fully exited, and now
			// bought back into. Whatever arrived today has not been at work yet,
			// so the index holds and the next session measures against it.
		}
		prevValue = value
		curve.Points = append(curve.Points, CurvePoint{
			Date:        date,
			MarketValue: value,
			NetInvested: netInvested,
			Index:       int64(math.Round(index)),
		})
	}

	if len(curve.Points) == 0 {
		curve.Unavailable = "no session in this period had anything at work to measure a return over"
		return curve, nil
	}
	summarize(&curve)
	return curve, nil
}

// summarize derives the headline figures from a finished curve.
func summarize(curve *CurrencyCurve) {
	points := curve.Points
	last := points[len(points)-1]

	twr := int64(math.Round(float64(last.Index-indexBase) / indexBase * 10000))
	curve.TWRBps = &twr

	// Annualizing needs a span to divide by, and a curve one session long has
	// none — reporting a rate from it would be inventing precision.
	if span := days(points[0].Date, last.Date); span > 0 {
		years := span / daysPerYear
		growth := float64(last.Index) / indexBase
		if years > 0 && growth > 0 {
			annualized := int64(math.Round((math.Pow(growth, 1/years) - 1) * 10000))
			curve.AnnualizedBps = &annualized
		}
	}

	peak := points[0].Index
	var deepest float64
	for _, p := range points {
		if p.Index > peak {
			peak = p.Index
		}
		if peak > 0 {
			if fall := float64(peak-p.Index) / float64(peak); fall > deepest {
				deepest = fall
			}
		}
	}
	drawdown := int64(math.Round(deepest * 10000))
	curve.MaxDrawdownBps = &drawdown
}

// daysPerYear matches the actual/365 convention the money-weighted return uses,
// so the two annualized figures on the same book are measured the same way.
const daysPerYear = 365.0

// days returns the calendar days between two YYYY-MM-DD dates.
func days(from, to string) float64 {
	a, errA := time.Parse(time.DateOnly, from)
	b, errB := time.Parse(time.DateOnly, to)
	if errA != nil || errB != nil {
		return 0
	}
	return b.Sub(a).Hours() / 24
}

// earliest returns the smallest value in m, or "" when it is empty.
func earliest(m map[string]string) string {
	out := ""
	for _, v := range m {
		if out == "" || v < out {
			out = v
		}
	}
	return out
}
