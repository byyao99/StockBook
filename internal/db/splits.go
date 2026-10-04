package db

import (
	"errors"
	"sort"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"stockbook/internal/models"
)

// UnadjustedSplit is a split the caller held shares across, reported as a
// warning that their ledger and the stored prices no longer agree about what a
// share is.
//
// Shares is what the ledger says was held at the close before the ex-date —
// that is, in pre-split shares, which is the number the warning is about. It is
// reported rather than a corrected figure on purpose: this system does not
// model splits, so it has no corrected figure to offer and will not invent one.
//
// Acknowledged is whether this user has already dealt with it. The prompt hides
// those; a holding's own page shows them, so a dismissal is reversible.
type UnadjustedSplit struct {
	InstrumentID string          `json:"instrument_id"`
	Symbol       string          `json:"symbol"`
	Name         string          `json:"name"`
	Currency     models.Currency `json:"currency"`
	Date         string          `json:"date"`
	RatioPpm     int64           `json:"ratio_ppm"`
	Shares       int64           `json:"shares"`
	Acknowledged bool            `json:"acknowledged"`
}

// SaveSplitEvents records the splits a provider reported for one instrument,
// replacing any already held for the same date.
//
// The upsert is what makes a refetch safe, exactly as it is for closes and
// distributions: a sync window overlaps what is already stored on purpose, so
// the same event arrives more than once and must land in one row.
func (d *DB) SaveSplitEvents(instrumentID string, events []models.SplitEvent) error {
	if len(events) == 0 {
		return nil
	}
	rows := make([]models.SplitEvent, 0, len(events))
	for _, e := range events {
		e.InstrumentID = instrumentID
		rows = append(rows, e)
	}
	return d.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "instrument_id"}, {Name: "date"}},
		DoUpdates: clause.AssignmentColumns([]string{"ratio_ppm", "updated_at"}),
	}).CreateInBatches(rows, 500).Error
}

// UnadjustedSplits reports the splits userID's book was holding shares across,
// newest first. includeAcknowledged keeps the ones already dismissed in the
// answer, flagged, rather than dropping them.
//
// **Holding across the date is the whole test**, and it is decided by replaying
// the ledger to the session before the ex-date rather than by looking at the
// position now. A holding sold since is still wrong in every figure covering
// the years it was held, which is exactly what the reports on /reports are for;
// scoping the warning to what is still owned would quietly exempt the history
// that the split corrupted. It is the same reasoning that decides a dividend
// entitlement, and it reuses the same replay.
//
// The day *before* is what is asked about, not the day itself. The ex-date is
// the first session that trades in new shares, so an entry dated on it was
// already recorded in them and needs no warning; an entry dated before it was
// not. Counting the ex-date itself would raise a warning on a book that bought
// in after the split and has nothing wrong with it.
func (d *DB) UnadjustedSplits(userID string, includeAcknowledged bool) ([]UnadjustedSplit, error) {
	entries := []ledgerShareRow{}
	err := d.db.Model(&models.Transaction{}).
		Joins("JOIN instruments ON instruments.id = transactions.instrument_id").
		Where("transactions.user_id = ?", userID).
		Select(`transactions.instrument_id AS instrument_id,
			transactions.side AS side,
			transactions.quantity AS quantity,
			transactions.traded_at AS traded_at,
			instruments.symbol AS symbol,
			instruments.name AS name,
			instruments.currency AS currency`).
		Order(qualifiedLedgerOrder).Scan(&entries).Error
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return []UnadjustedSplit{}, nil
	}

	held := map[string][]ledgerShareRow{}
	for _, e := range entries {
		held[e.InstrumentID] = append(held[e.InstrumentID], e)
	}
	ids := make([]string, 0, len(held))
	for id := range held {
		ids = append(ids, id)
	}

	events := []models.SplitEvent{}
	if err := d.db.Where("instrument_id IN ?", ids).Find(&events).Error; err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return []UnadjustedSplit{}, nil
	}

	acks := []models.SplitAcknowledgement{}
	if err := d.db.Where("user_id = ?", userID).Find(&acks).Error; err != nil {
		return nil, err
	}
	acknowledged := map[string]bool{}
	for _, a := range acks {
		acknowledged[a.InstrumentID+"|"+a.Date] = true
	}

	out := []UnadjustedSplit{}
	for _, event := range events {
		ledger := held[event.InstrumentID]
		if len(ledger) == 0 {
			continue
		}
		before, err := dayBefore(event.Date)
		if err != nil {
			// A stored date this system cannot parse is not something a reader
			// can act on, and guessing which side of it the ledger falls would
			// be worse than saying nothing.
			continue
		}
		shares := sharesOn(ledger, before)
		if shares <= 0 {
			continue
		}
		done := acknowledged[event.InstrumentID+"|"+event.Date]
		if done && !includeAcknowledged {
			continue
		}
		out = append(out, UnadjustedSplit{
			InstrumentID: event.InstrumentID,
			Symbol:       ledger[0].Symbol,
			Name:         ledger[0].Name,
			Currency:     ledger[0].Currency,
			Date:         event.Date,
			RatioPpm:     event.RatioPpm,
			Shares:       shares,
			Acknowledged: done,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date > out[j].Date
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out, nil
}

// AcknowledgeSplit marks one split as dealt with for one user.
//
// The event has to exist: acknowledging one that does not would write a row
// that silences nothing and report success, which is the kind of quiet no-op
// that is only discovered when the warning it was meant to dismiss comes back.
func (d *DB) AcknowledgeSplit(userID, instrumentID, date string) error {
	var event models.SplitEvent
	err := d.db.Where("instrument_id = ? AND date = ?", instrumentID, date).First(&event).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return d.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&models.SplitAcknowledgement{
		UserID:       userID,
		InstrumentID: instrumentID,
		Date:         date,
	}).Error
}

// ForgetSplitAcknowledgement undoes an acknowledgement, bringing the warning
// back. Dismissing a warning about a genuinely broken history by accident must
// not be permanent.
func (d *DB) ForgetSplitAcknowledgement(userID, instrumentID, date string) error {
	return d.db.Where("user_id = ? AND instrument_id = ? AND date = ?", userID, instrumentID, date).
		Delete(&models.SplitAcknowledgement{}).Error
}

// dayBefore returns the calendar day before a YYYY-MM-DD date.
func dayBefore(date string) (string, error) {
	day, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return "", err
	}
	return day.AddDate(0, 0, -1).Format(time.DateOnly), nil
}
