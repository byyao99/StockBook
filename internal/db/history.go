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
