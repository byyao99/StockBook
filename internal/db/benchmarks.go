package db

import (
	"errors"
	"sort"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"stockbook/internal/models"
)

// BenchmarkView is a benchmark joined with the instrument it names, which is
// all a caller ever wants: an instrument id on its own says nothing a settings
// page or a chart legend could render.
type BenchmarkView struct {
	Currency     models.Currency `json:"currency"`
	InstrumentID string          `json:"instrument_id"`
	Symbol       string          `json:"symbol"`
	Name         string          `json:"name"`
}

// Benchmarks returns the instruments userID measures each currency against.
//
// The result is only what was actually chosen — there is no merging of defaults
// the way FeeProfiles does it, because there is no default to merge. See
// models.Benchmark for why.
func (d *DB) Benchmarks(userID string) ([]BenchmarkView, error) {
	out := []BenchmarkView{}
	err := d.db.Model(&models.Benchmark{}).
		Joins("JOIN instruments ON instruments.id = benchmarks.instrument_id").
		Where("benchmarks.user_id = ?", userID).
		Select(`benchmarks.currency AS currency,
			benchmarks.instrument_id AS instrument_id,
			instruments.symbol AS symbol,
			instruments.name AS name`).
		Order("benchmarks.currency asc").
		Scan(&out).Error
	return out, err
}

// benchmarkFor returns the benchmark userID has chosen for one currency, or
// false when there is none.
func (d *DB) benchmarkFor(userID string, currency models.Currency) (BenchmarkView, bool, error) {
	var view BenchmarkView
	err := d.db.Model(&models.Benchmark{}).
		Joins("JOIN instruments ON instruments.id = benchmarks.instrument_id").
		Where("benchmarks.user_id = ? AND benchmarks.currency = ?", userID, currency).
		Select(`benchmarks.currency AS currency,
			benchmarks.instrument_id AS instrument_id,
			instruments.symbol AS symbol,
			instruments.name AS name`).
		Take(&view).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return BenchmarkView{}, false, nil
	}
	if err != nil {
		return BenchmarkView{}, false, err
	}
	return view, true, nil
}

// ErrBenchmarkCurrency is returned when a benchmark is quoted in a currency
// other than the one it would measure. Handlers map it to HTTP 409.
//
// This is the same rule that refuses a quote in the wrong currency, applied to
// the one place a user could otherwise break it by hand: a TWD book measured
// against VOO would compare a return against an exchange rate and call the
// difference performance.
var ErrBenchmarkCurrency = errors.New("a benchmark must be quoted in the currency it measures")

// SaveBenchmark records the instrument userID measures a currency against,
// replacing whatever was there.
func (d *DB) SaveBenchmark(userID string, currency models.Currency, instrumentID string) error {
	instrument, err := d.GetInstrument(instrumentID)
	if err != nil {
		return err
	}
	if instrument.Currency != currency {
		return ErrBenchmarkCurrency
	}
	return d.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "currency"}},
		DoUpdates: clause.AssignmentColumns([]string{"instrument_id", "updated_at"}),
	}).Create(&models.Benchmark{
		UserID:       userID,
		Currency:     currency,
		InstrumentID: instrumentID,
	}).Error
}

// DeleteBenchmark stops measuring a currency against anything.
func (d *DB) DeleteBenchmark(userID string, currency models.Currency) error {
	return d.db.Where("user_id = ? AND currency = ?", userID, currency).
		Delete(&models.Benchmark{}).Error
}

// EarliestBenchmarkNeed returns the first day an instrument's closes are needed
// because somebody measures their book against it, or "" when nobody does.
//
// A benchmark is compared session by session against the book it measures, so
// its prices have to reach back as far as that book's own first trade — not to
// the day the benchmark was chosen, which is usually long after. Anything less
// and the comparison silently starts partway through.
//
// Like the other two answers feeding EarliestHistoryNeeded this spans every
// user, because instruments are shared master data and the prices it brings back
// are public either way.
func (d *DB) EarliestBenchmarkNeed(instrumentID string) (string, error) {
	var first models.Transaction
	err := d.db.
		Joins("JOIN benchmarks ON benchmarks.user_id = transactions.user_id").
		Where("benchmarks.instrument_id = ?", instrumentID).
		Order("transactions.traded_at asc").
		First(&first).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return first.TradedAt.UTC().Format(time.DateOnly), nil
}

// benchmarkTrack is a benchmark's total-return path: its stored closes plus the
// distributions it paid along the way.
//
// **Total return, not price.** The book's own index already includes the
// dividends it received — a payout leaves the ledger as a negative flow, so the
// index rises by it — and comparing that against a benchmark's bare price would
// dock the benchmark every distribution it ever made while crediting the book
// with every one of its own. That is not a small effect: 0050 yields around 3%
// and VOO around 1.3%, so over a few years the comparison would flatter the
// reader by a compounding margin, in the one direction they would never think
// to question. The distributions are already stored — the price sync asks for
// them in the same request — so there is no excuse for using the price series.
type benchmarkTrack struct {
	prices    priceSeries
	dividends map[string]int64
	exDates   []string
}

// step returns the total-return factor between two sessions' closes, and
// whether both ends are priced. A distribution going ex in (from, to] is cash
// the holder had, so it counts toward the return of that step exactly as the
// price change does.
func (b benchmarkTrack) step(from, to string) (float64, bool) {
	before, ok := b.prices.on(from)
	if !ok || before <= 0 {
		return 0, false
	}
	after, ok := b.prices.on(to)
	if !ok {
		return 0, false
	}
	var paid int64
	lo := sort.SearchStrings(b.exDates, from)
	for lo < len(b.exDates) && b.exDates[lo] <= from {
		lo++ // exclusive lower bound: a distribution on `from` belonged to the step before
	}
	for i := lo; i < len(b.exDates) && b.exDates[i] <= to; i++ {
		paid += b.dividends[b.exDates[i]]
	}
	return float64(after+paid) / float64(before), true
}

// benchmarkTrackFor loads one instrument's closes and distributions up to `to`.
func (d *DB) benchmarkTrackFor(instrumentID, to string) (benchmarkTrack, error) {
	track := benchmarkTrack{dividends: map[string]int64{}}

	rows, err := d.DailyCloseSeries(instrumentID, "0000-01-01", to)
	if err != nil {
		return track, err
	}
	track.prices = priceSeries{
		closes: make(map[string]int64, len(rows)),
		dates:  make([]string, 0, len(rows)),
	}
	for _, r := range rows {
		track.prices.closes[r.Date] = r.Close
		track.prices.dates = append(track.prices.dates, r.Date)
	}

	events := []models.DividendEvent{}
	if err := d.db.Where("instrument_id = ? AND ex_date <= ?", instrumentID, to).
		Order("ex_date asc").Find(&events).Error; err != nil {
		return track, err
	}
	for _, e := range events {
		// Two rows for one ex-date cannot happen — it is half the primary key —
		// but adding rather than assigning costs nothing and cannot lose one.
		track.dividends[e.ExDate] += e.Amount
		track.exDates = append(track.exDates, e.ExDate)
	}
	return track, nil
}

// covers reports whether the track can price the first session of a window.
// Prices are carried forward but never backwards, so a series starting after the
// book did cannot measure it, and half a comparison is worse than none.
func (b benchmarkTrack) covers(date string) bool {
	price, ok := b.prices.on(date)
	return ok && price > 0
}

// trackFor resolves the benchmark a curve should be measured against, stamping
// the curve with what it found and reporting whether there is one to run.
//
// The two refusals are kept apart because they are different problems with
// different fixes: nothing chosen is answered on the settings page, while a
// benchmark whose stored prices start after the book did is answered by a
// sync. Collapsing them into one "unavailable" would leave a reader guessing
// which.
func (d *DB) trackFor(curve *CurrencyCurve, userID string, currency models.Currency, firstSession, to string) (benchmarkTrack, bool, error) {
	view, ok, err := d.benchmarkFor(userID, currency)
	if err != nil {
		return benchmarkTrack{}, false, err
	}
	if !ok {
		curve.BenchmarkUnavailable = "no benchmark chosen for " + string(currency) + " — pick one on the Account page"
		return benchmarkTrack{}, false, nil
	}
	curve.Benchmark = &view

	track, err := d.benchmarkTrackFor(view.InstrumentID, to)
	if err != nil {
		return benchmarkTrack{}, false, err
	}
	if !track.covers(firstSession) {
		curve.BenchmarkUnavailable = "no stored price for " + view.Symbol +
			" on or before " + firstSession + "; sync prices first"
		return benchmarkTrack{}, false, nil
	}
	return track, true, nil
}
