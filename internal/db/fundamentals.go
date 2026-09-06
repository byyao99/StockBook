package db

import (
	"time"

	"gorm.io/gorm/clause"

	"stockbook/internal/models"
)

// SaveFinancialFacts writes reported figures for one instrument, replacing any
// already held for the same metric, cadence and period.
//
// The upsert is what makes a refetch safe, exactly as it is for a daily close: a
// window is refetched with overlap because a company restates a figure now and
// then, and inserting blindly would either fail on the key or leave two rows for
// one quarter where every reader expects one.
//
// Writing nothing is not an error. A company that has published nothing new
// since the last sync is the ordinary case — most of the year, for most of them.
func (d *DB) SaveFinancialFacts(instrumentID string, facts []models.FinancialFact) error {
	if len(facts) == 0 {
		return nil
	}
	rows := make([]models.FinancialFact, 0, len(facts))
	for _, fact := range facts {
		fact.InstrumentID = instrumentID
		rows = append(rows, fact)
	}
	return d.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "instrument_id"}, {Name: "metric"},
			{Name: "period_type"}, {Name: "as_of_date"},
		},
		DoUpdates: clause.AssignmentColumns([]string{"value", "currency", "updated_at"}),
	}).CreateInBatches(rows, 500).Error
}

// FinancialPeriod is one reporting period with every figure held for it.
//
// The three metrics are pointers because a period the provider has no figure for
// is unknown, not zero: a company that has not yet reported its EPS has not
// earned nothing. The UI renders a nil as an em dash, exactly as it does an
// unpriced holding.
//
// Currency is the one the figures were reported in, carried on every period
// rather than once for the instrument, because a company that changes reporting
// currency does so between periods and folding them together would silently
// compare two different units.
type FinancialPeriod struct {
	AsOfDate   string          `json:"as_of_date"`
	PeriodType string          `json:"period_type"`
	Currency   models.Currency `json:"currency"`
	Revenue    *int64          `json:"revenue"`
	NetIncome  *int64          `json:"net_income"`
	DilutedEPS *int64          `json:"diluted_eps"`
}

// FinancialReport returns one instrument's reported periods at one cadence, in
// chronological order.
//
// The rows are pivoted here rather than in the handler because the storage shape
// (one row per metric per period) and the reading shape (one row per period) are
// genuinely different, and the join between them belongs next to the query that
// knows the key.
//
// periodType is the provider's own stamp ("3M" or "12M") rather than a word of
// this system's, because it is what the rows are keyed by and translating it
// here would put two vocabularies in one query.
func (d *DB) FinancialReport(instrumentID, periodType string) ([]FinancialPeriod, error) {
	rows := []models.FinancialFact{}
	err := d.db.Where("instrument_id = ? AND period_type = ?", instrumentID, periodType).
		Order("as_of_date asc").Find(&rows).Error
	if err != nil {
		return nil, err
	}

	periods := []FinancialPeriod{}
	index := map[string]int{}
	for _, fact := range rows {
		at, ok := index[fact.AsOfDate]
		if !ok {
			periods = append(periods, FinancialPeriod{
				AsOfDate:   fact.AsOfDate,
				PeriodType: fact.PeriodType,
				Currency:   fact.Currency,
			})
			at = len(periods) - 1
			index[fact.AsOfDate] = at
		}
		value := fact.Value
		switch fact.Metric {
		case models.MetricRevenue:
			periods[at].Revenue = &value
		case models.MetricNetIncome:
			periods[at].NetIncome = &value
		case models.MetricDilutedEPS:
			periods[at].DilutedEPS = &value
		}
	}
	return periods, nil
}

// CountFinancialFacts reports how many figures are held for an instrument. It
// exists so a sync can say what it has accumulated rather than only what one
// call added — a run that writes nothing but already holds five years is
// healthy, and only the second number says so.
func (d *DB) CountFinancialFacts(instrumentID string) (int64, error) {
	var n int64
	err := d.db.Model(&models.FinancialFact{}).
		Where("instrument_id = ?", instrumentID).Count(&n).Error
	return n, err
}

// MarkNewsChecked stamps when a set of instruments last had their news
// collected. Failures do not call this, so a source that is down is retried on
// the next press rather than being suppressed for the freshness window — the
// same rule that leaves a failed quote fetch's timestamps alone.
func (d *DB) MarkNewsChecked(instrumentIDs []string, at time.Time) error {
	if len(instrumentIDs) == 0 {
		return nil
	}
	return d.db.Model(&models.Instrument{}).
		Where("id IN ?", instrumentIDs).
		Update("news_checked_at", at).Error
}

// MarkFundamentalsChecked stamps when one instrument's reported figures were
// last fetched, on the same terms as MarkNewsChecked.
func (d *DB) MarkFundamentalsChecked(instrumentID string, at time.Time) error {
	return d.db.Model(&models.Instrument{}).
		Where("id = ?", instrumentID).
		Update("fundamentals_checked_at", at).Error
}
