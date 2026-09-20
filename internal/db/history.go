package db

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"stockbook/internal/models"
)

// SaveDailyCloses writes a run of closing prices for one instrument, replacing
// any it already holds for the same days.
//
// The upsert is what makes a re-fetch safe. A window is normally refetched from
// the last day already stored, so its first day arrives twice, and the provider
// does occasionally revise a session after the fact. Inserting blindly would
// either fail on the key or, worse, leave two rows for one day where every
// consumer expects one.
//
// Writing nothing is not an error: a window covering only a weekend has no
// sessions in it, and there is nothing wrong with that.
func (d *DB) SaveDailyCloses(instrumentID string, closes []models.DailyClose) error {
	if len(closes) == 0 {
		return nil
	}
	rows := make([]models.DailyClose, 0, len(closes))
	for _, c := range closes {
		c.InstrumentID = instrumentID
		rows = append(rows, c)
	}
	return d.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "instrument_id"}, {Name: "date"}},
		DoUpdates: clause.AssignmentColumns([]string{"close", "updated_at"}),
	}).CreateInBatches(rows, 500).Error
}

// LatestStoredClose returns the most recent day held for an instrument, or ""
// when none is. Callers use it to fetch only what they are missing rather than
// re-downloading years on every sync.
// The scan target is a pointer because MAX over no rows is NULL, which is the
// ordinary case on a first sync and must read as "nothing stored" rather than
// as a failure.
func (d *DB) LatestStoredClose(instrumentID string) (string, error) {
	var date *string
	err := d.db.Model(&models.DailyClose{}).
		Where("instrument_id = ?", instrumentID).
		Select("MAX(date)").Scan(&date).Error
	if err != nil || date == nil {
		return "", err
	}
	return *date, nil
}

// EarliestTradedAt returns the date of the first ledger entry against an
// instrument, across every user, or "" when nothing has been traded in it.
//
// This is how far back history is worth having: prices before the first trade
// value nothing, and instruments are shared master data, so the answer cannot be
// scoped to one caller.
// The earliest row is read through the model rather than as a bare
// MIN(traded_at): the pure-Go SQLite driver hands an aggregate back as the
// string it stored, which will not scan into a time.Time, and GORM's own column
// mapping is what knows how to read the timestamp it wrote.
func (d *DB) EarliestTradedAt(instrumentID string) (string, error) {
	var first models.Transaction
	err := d.db.Where("instrument_id = ?", instrumentID).
		Order("traded_at asc").First(&first).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return first.TradedAt.UTC().Format(time.DateOnly), nil
}

// EarliestPlanStart returns the start date of the earliest savings plan buying
// an instrument, across every user, or "" when no plan names it.
//
// StartedOn is stored as YYYY-MM-DD, which sorts lexicographically into
// chronological order, so MIN answers directly — unlike the timestamp
// EarliestTradedAt has to read through the model.
func (d *DB) EarliestPlanStart(instrumentID string) (string, error) {
	var date *string
	err := d.db.Model(&models.RecurringPlan{}).
		Where("instrument_id = ?", instrumentID).
		Select("MIN(started_on)").Scan(&date).Error
	if err != nil || date == nil {
		return "", err
	}
	return *date, nil
}

// EarliestHistoryNeeded returns the first day an instrument's closes are worth
// having, or "" when nothing needs them.
//
// Three separate things want history, and the earliest of them wins:
//
//   - a **trade**, which is the obvious answer and was the only one for a long
//     time: prices before an instrument was first bought value nothing;
//   - a **savings plan**, which comes due on dates of its own and whose prompt
//     estimates the shares an instalment bought from that day's close. The
//     ordinary way to start a plan is before the first purchase, so the
//     instrument has no trades at all and the first-trade rule declined to fetch
//     it — leaving every prompt it raised with no price to estimate from,
//     permanently;
//   - a **benchmark**, which is compared session by session against the book it
//     measures and so needs prices reaching back to that book's own first trade,
//     not to the day it was chosen. An instrument nobody trades is the normal
//     case for one: the whole point is to hold something else instead.
//
// All three answers span every user's book, exactly as EarliestTradedAt does and
// for the same reason: instruments are shared master data, and what comes back
// is public market data either way.
func (d *DB) EarliestHistoryNeeded(instrumentID string) (string, error) {
	traded, err := d.EarliestTradedAt(instrumentID)
	if err != nil {
		return "", err
	}
	planned, err := d.EarliestPlanStart(instrumentID)
	if err != nil {
		return "", err
	}
	benchmarked, err := d.EarliestBenchmarkNeed(instrumentID)
	if err != nil {
		return "", err
	}
	return earliestDate(traded, planned, benchmarked), nil
}

// earliestDate returns the smallest non-empty YYYY-MM-DD among its arguments,
// or "" when they are all empty. Dates in this form sort lexicographically into
// chronological order, so a string compare is the whole comparison.
func earliestDate(dates ...string) string {
	out := ""
	for _, date := range dates {
		if date == "" {
			continue
		}
		if out == "" || date < out {
			out = date
		}
	}
	return out
}

// DailyCloseSeries returns one instrument's closes between from and to
// inclusive, in date order. Both bounds are YYYY-MM-DD.
func (d *DB) DailyCloseSeries(instrumentID, from, to string) ([]models.DailyClose, error) {
	rows := []models.DailyClose{}
	err := d.db.Where("instrument_id = ? AND date >= ? AND date <= ?", instrumentID, from, to).
		Order("date asc").Find(&rows).Error
	return rows, err
}

// CountDailyCloses reports how many sessions are held for an instrument. It
// exists so a sync can report what it actually accumulated rather than only
// what one call added.
func (d *DB) CountDailyCloses(instrumentID string) (int64, error) {
	var n int64
	err := d.db.Model(&models.DailyClose{}).
		Where("instrument_id = ?", instrumentID).Count(&n).Error
	return n, err
}
