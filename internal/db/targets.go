package db

import (
	"errors"
	"fmt"
	"sort"

	"gorm.io/gorm"

	"stockbook/internal/models"
)

// ErrInvalidTargets is returned when a set of target weights cannot be saved as
// given: a weight out of range, an instrument named twice or not on file, or a
// currency whose weights do not add up to 100%. The wrapped message says which;
// handlers map it to 400.
var ErrInvalidTargets = errors.New("invalid target allocation")

// TargetView is a target weight joined with the instrument it names. The
// currency comes from the instrument, which is what groups the targets into
// one plan per currency.
type TargetView struct {
	InstrumentID string          `json:"instrument_id"`
	Symbol       string          `json:"symbol"`
	Name         string          `json:"name"`
	Currency     models.Currency `json:"currency"`
	WeightBps    int64           `json:"weight_bps"`
	// LastPrice is the instrument's quote, nil when it has none. Carried so the
	// planner can size a purchase of something targeted but not yet held,
	// which no position on the page would otherwise price.
	LastPrice *int64 `json:"last_price"`
}

// TargetInput is one weight in a set being saved.
type TargetInput struct {
	InstrumentID string
	WeightBps    int64
}

// Targets returns userID's target weights, grouped by currency and largest
// first within it.
func (d *DB) Targets(userID string) ([]TargetView, error) {
	return targetsOf(d.db, userID)
}

func targetsOf(tx *gorm.DB, userID string) ([]TargetView, error) {
	out := []TargetView{}
	err := tx.Model(&models.TargetWeight{}).
		Joins("JOIN instruments ON instruments.id = target_weights.instrument_id").
		Where("target_weights.user_id = ?", userID).
		Select(`target_weights.instrument_id AS instrument_id,
			instruments.symbol AS symbol,
			instruments.name AS name,
			instruments.currency AS currency,
			target_weights.weight_bps AS weight_bps,
			instruments.last_price AS last_price`).
		Order("instruments.currency asc, target_weights.weight_bps desc, instruments.symbol asc").
		Scan(&out).Error
	return out, err
}

// SaveTargets replaces userID's whole set of target weights with targets.
//
// The whole set, because a target only means something beside the others in
// its currency: saving one weight at a time would pass through states that add
// up to 130%, and a planner reading any of them would suggest nonsense. So the
// set is validated as one — **every currency present must add up to exactly
// 100%** — and written in one transaction, and an empty set clears the lot.
// A currency with no targets at all is simply unplanned, which is a valid
// answer; one with 95% planned is not, because the planner would have to
// guess where the other 5% was meant to go.
func (d *DB) SaveTargets(userID string, targets []TargetInput) ([]TargetView, error) {
	var saved []TargetView
	err := d.db.Transaction(func(tx *gorm.DB) error {
		sums := map[models.Currency]int64{}
		seen := map[string]bool{}
		for _, t := range targets {
			if t.WeightBps <= 0 || t.WeightBps > models.FullWeightBps {
				return fmt.Errorf("%w: a weight must be more than 0%% and at most 100%%", ErrInvalidTargets)
			}
			if seen[t.InstrumentID] {
				return fmt.Errorf("%w: an instrument appears twice", ErrInvalidTargets)
			}
			seen[t.InstrumentID] = true

			var instrument models.Instrument
			if err := tx.First(&instrument, "id = ?", t.InstrumentID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return fmt.Errorf("%w: instrument %s is not on file", ErrInvalidTargets, t.InstrumentID)
				}
				return err
			}
			sums[instrument.Currency] += t.WeightBps
		}

		currencies := make([]string, 0, len(sums))
		for c := range sums {
			currencies = append(currencies, string(c))
		}
		sort.Strings(currencies)
		for _, c := range currencies {
			if sum := sums[models.Currency(c)]; sum != models.FullWeightBps {
				return fmt.Errorf("%w: %s targets add up to %.2f%%, not 100%%",
					ErrInvalidTargets, c, float64(sum)/100)
			}
		}

		if err := tx.Where("user_id = ?", userID).Delete(&models.TargetWeight{}).Error; err != nil {
			return err
		}
		for _, t := range targets {
			row := models.TargetWeight{UserID: userID, InstrumentID: t.InstrumentID, WeightBps: t.WeightBps}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}

		var err error
		saved, err = targetsOf(tx, userID)
		return err
	})
	return saved, err
}
