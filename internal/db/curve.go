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
	// BenchmarkIndex and BenchmarkValue are the same two figures for the
	// benchmark: what a notional 100 in it would have grown to, and what this
	// book's own money would be worth had it gone there instead.
	//
	// They are plain zeros rather than pointers when there is no benchmark,
	// which is the one place this system does not distinguish unknown from
	// zero in a field. The gate is CurrencyCurve.Benchmark being nil, which a
	// reader has to consult anyway to know what it is being compared against —
	// and two pointers per session across a decade of them is a lot of
	// allocation to re-state something the envelope already says.
	BenchmarkIndex int64 `json:"benchmark_index"`
	BenchmarkValue int64 `json:"benchmark_value"`
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
	// Benchmark is what this currency's book is measured against, or nil when
	// the user has chosen nothing. Its figures answer the one question no other
	// report here can: whether any of this beat buying the index instead.
	Benchmark              *BenchmarkView `json:"benchmark,omitempty"`
	BenchmarkTWRBps        *int64         `json:"benchmark_twr_bps"`
	BenchmarkAnnualizedBps *int64         `json:"benchmark_annualized_bps"`
	// BenchmarkValue is what the book would be worth now had the same money
	// arrived on the same days into the benchmark instead — the visceral form
	// of the same comparison, and the reason the flows are tracked rather than
	// only the index.
	BenchmarkValue *int64 `json:"benchmark_value"`
	// BenchmarkExhausted is set when the withdrawals this book made would have
	// emptied the benchmark before the period ended. The synthetic account is
	// floored at zero because shares nobody owns cannot be sold, so from that
	// point the two are no longer running the same money and the value
	// comparison has stopped meaning anything. The index comparison is
	// unaffected: it never had a balance to run out of.
	BenchmarkExhausted bool `json:"benchmark_exhausted"`
	// BenchmarkUnavailable explains in words why there is no comparison, and is
	// empty when there is one. "Not chosen" and "chosen but unpriced" are
	// different problems with different fixes, and a reader can act on either.
	BenchmarkUnavailable string `json:"benchmark_unavailable,omitempty"`
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
	Quantity     int64
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
//
// # The benchmark
//
// When the user has chosen one for this currency, the same loop runs a second
// account beside the book: it starts with the book's own value on the first
// plotted session, grows by the benchmark's **total** return each day, and takes
// the same flows on the same days. Its recursion is the book's, rearranged —
// the curve's index is chained from (value - flow) / previous value, so
// value = previous * (1 + r) + flow is the identity the book itself satisfies.
// Running the benchmark by that same identity is what makes the two ends
// comparable instead of merely adjacent, and it needs no cash balance: a flow is
// added at the close, exactly the convention the index already assumes.
//
// This deliberately answers a narrower question than "would you be richer".
// Money taken out of this book went somewhere the system does not model, so the
// claim is only that the same contributions, timed the same way, would have
// grown to this instead.
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
		curve, err := d.currencyCurve(userID, currency, ledger, from, to)
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

// currencyCurve builds one currency's curve from its slice of the ledger.
func (d *DB) currencyCurve(userID string, currency models.Currency, ledger []curveTx, from, to string) (CurrencyCurve, error) {
	curve := CurrencyCurve{Currency: currency, Points: []CurvePoint{}}

	priced, err := d.priceLedger(ledger, to)
	if err != nil {
		return curve, err
	}
	curve.Instruments = priced.instruments()
	curve.WithoutHistory = priced.withoutHistory
	series, sessions, firstTraded := priced.series, priced.sessions, priced.firstTraded

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

	track, benchmarking, err := d.trackFor(&curve, userID, currency, axis[0], to)
	if err != nil {
		return curve, err
	}

	held := map[string]models.PositionState{}
	var (
		next        int // the next ledger entry not yet folded in
		netInvested int64
		prevValue   int64
		index       = float64(indexBase)
		started     bool

		// The benchmark runs the same money on the same days through a
		// different holding. benchValue follows the book's own recursion
		// exactly — yesterday's value, grown by the benchmark's return, plus
		// today's flow — which is what makes the two comparable rather than
		// merely adjacent.
		prevDate   string
		benchIndex = float64(indexBase)
		benchValue float64
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
			value += models.Gross(state.Quantity, close)
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
		// Both benchmark figures stay at zero when no comparison ran, which is
		// what CurvePoint promises and what lets the frontend gate on the
		// envelope alone. Leaving the index sitting at its base instead would
		// hand every reader a flat line at 100 that nothing had measured.
		var benchIndexOut, benchValueOut int64
		if benchmarking {
			if prevDate == "" {
				// The first plotted session starts the benchmark with the same
				// capital the book had at that close, so day one is a tie by
				// construction and every difference after it was earned.
				benchValue = float64(value)
			} else if factor, ok := track.step(prevDate, date); ok {
				benchIndex *= factor
				benchValue = benchValue*factor + float64(flow)
				if benchValue < 0 {
					// Shares nobody owns cannot be sold: the synthetic account
					// is empty rather than overdrawn, and from here the two
					// sides are no longer running the same money.
					benchValue = 0
					curve.BenchmarkExhausted = true
				}
			}
			prevDate = date
			benchIndexOut = int64(math.Round(benchIndex))
			benchValueOut = int64(math.Round(benchValue))
		}

		prevValue = value
		curve.Points = append(curve.Points, CurvePoint{
			Date:           date,
			MarketValue:    value,
			NetInvested:    netInvested,
			Index:          int64(math.Round(index)),
			BenchmarkIndex: benchIndexOut,
			BenchmarkValue: benchValueOut,
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
	span := days(points[0].Date, last.Date)

	twr := returnOf(last.Index)
	curve.TWRBps = &twr
	curve.AnnualizedBps = annualize(last.Index, span)

	// The benchmark's two figures are derived the same way from the same span,
	// which is the point: a comparison between numbers measured differently is
	// not a comparison. They are stamped only when a benchmark actually ran —
	// BenchmarkIndex sits at its base when none did, which would read as a flat
	// 0% rather than as the absence it is.
	if curve.Benchmark != nil && curve.BenchmarkUnavailable == "" {
		benchTWR := returnOf(last.BenchmarkIndex)
		curve.BenchmarkTWRBps = &benchTWR
		curve.BenchmarkAnnualizedBps = annualize(last.BenchmarkIndex, span)
		value := last.BenchmarkValue
		curve.BenchmarkValue = &value
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

// returnOf is an index's growth over the whole period, in basis points.
func returnOf(index int64) int64 {
	return int64(math.Round(float64(index-indexBase) / indexBase * 10000))
}

// annualize turns an index's total growth into a yearly rate, or nil when the
// span is too short to divide by. A curve one session long has no span, and
// reporting a rate from it would be inventing precision.
func annualize(index int64, span float64) *int64 {
	if span <= 0 {
		return nil
	}
	years := span / daysPerYear
	growth := float64(index) / indexBase
	if years <= 0 || growth <= 0 {
		return nil
	}
	rate := int64(math.Round((math.Pow(growth, 1/years) - 1) * 10000))
	return &rate
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
