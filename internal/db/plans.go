package db

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"stockbook/internal/models"
)

// purchaseClaimWindow is how far from a due date a buy may be dated and still
// be taken as that instalment.
//
// A savings plan executes on its day or the next session, and a user recording
// it from a statement dates it when the money moved. Ten days covers both ends
// of that without reaching the next instalment: the tightest schedule this
// allows is three a month, which is ten days apart.
const purchaseClaimWindow = 10 * 24 * time.Hour

// PlanView is a plan joined with the instrument it buys.
type PlanView struct {
	models.RecurringPlan
	Symbol   string          `json:"symbol"`
	Name     string          `json:"name"`
	Currency models.Currency `json:"currency"`
}

// PendingPurchase is an instalment that came due and has no buy against it.
//
// It is a prompt, never a posting. EstimatedShares and EstimatedPrice are
// derived from the stored close on the due date and are the best guess
// available, not a claim: the fill happened at whatever the market did that
// morning, and only a statement says what. They are nil when no close is
// stored, in which case the reader supplies both — unknown is not zero here
// either.
type PendingPurchase struct {
	PlanID          string          `json:"plan_id"`
	InstrumentID    string          `json:"instrument_id"`
	Symbol          string          `json:"symbol"`
	Name            string          `json:"name"`
	Currency        models.Currency `json:"currency"`
	DueOn           string          `json:"due_on"`
	Amount          int64           `json:"amount"`
	EstimatedPrice  *int64          `json:"estimated_price"`
	EstimatedShares *int64          `json:"estimated_shares"`
}

// ParsePlanDays reads a comma-separated day list into a sorted, deduplicated
// slice, refusing anything that is not a day of the month.
func ParsePlanDays(raw string) ([]int, error) {
	seen := map[int]bool{}
	days := []int{}
	for _, field := range strings.Split(raw, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		day, err := strconv.Atoi(field)
		if err != nil || day < 1 || day > models.MaxPlanDay {
			return nil, errors.New("each day must be a number from 1 to 31")
		}
		if seen[day] {
			continue
		}
		seen[day] = true
		days = append(days, day)
	}
	if len(days) == 0 {
		return nil, errors.New("a plan needs at least one day of the month")
	}
	sort.Ints(days)
	return days, nil
}

// FormatPlanDays renders a parsed day list back to storage form.
func FormatPlanDays(days []int) string {
	parts := make([]string, 0, len(days))
	for _, d := range days {
		parts = append(parts, strconv.Itoa(d))
	}
	return strings.Join(parts, ",")
}

// dueDates lists every instalment a plan has reached, oldest first.
//
// A day past the end of a short month clamps to that month's last day rather
// than being skipped. A plan for the 31st is a plan to buy every month, and
// dropping February would quietly make it eleven purchases a year — which the
// reader would only notice by counting.
func dueDates(plan models.RecurringPlan, until time.Time) ([]string, error) {
	days, err := ParsePlanDays(plan.DaysOfMonth)
	if err != nil {
		return nil, err
	}
	start, err := time.Parse(time.DateOnly, plan.StartedOn)
	if err != nil {
		return nil, err
	}
	stop := until.UTC()
	if plan.EndedOn != "" {
		ended, err := time.Parse(time.DateOnly, plan.EndedOn)
		if err != nil {
			return nil, err
		}
		if ended.Before(stop) {
			stop = ended
		}
	}

	dates := []string{}
	month := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC)
	for !month.After(stop) {
		lastDay := month.AddDate(0, 1, -1).Day()
		for _, day := range days {
			if day > lastDay {
				day = lastDay
			}
			at := time.Date(month.Year(), month.Month(), day, 0, 0, 0, 0, time.UTC)
			if at.Before(start) || at.After(stop) {
				continue
			}
			dates = append(dates, at.Format(time.DateOnly))
		}
		month = month.AddDate(0, 1, 0)
	}
	sort.Strings(dates)
	return dates, nil
}

// ListPlans returns userID's savings plans, running ones first.
func (d *DB) ListPlans(userID string) ([]PlanView, error) {
	rows := []PlanView{}
	err := d.db.Model(&models.RecurringPlan{}).
		Joins("JOIN instruments ON instruments.id = recurring_plans.instrument_id").
		Where("recurring_plans.user_id = ?", userID).
		Select(`recurring_plans.*,
			instruments.symbol AS symbol,
			instruments.name AS name,
			instruments.currency AS currency`).
		Order("recurring_plans.ended_on asc, instruments.symbol asc").
		Scan(&rows).Error
	return rows, err
}

// CreatePlan stores a new savings plan.
func (d *DB) CreatePlan(plan models.RecurringPlan) (models.RecurringPlan, error) {
	if err := d.db.Create(&plan).Error; err != nil {
		return models.RecurringPlan{}, err
	}
	return plan, nil
}

// EndPlan stops a plan without deleting it, so the purchases it prompted keep
// the record of why they are spaced as they are.
func (d *DB) EndPlan(userID, id, on string) error {
	res := d.db.Model(&models.RecurringPlan{}).
		Where("id = ? AND user_id = ?", id, userID).
		Update("ended_on", on)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePlan removes a plan outright, for one entered by mistake.
func (d *DB) DeletePlan(userID, id string) error {
	res := d.db.Where("id = ? AND user_id = ?", id, userID).
		Delete(&models.RecurringPlan{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// PendingPurchases reports instalments that came due with nothing recorded
// against them, newest first.
//
// A due date is answered by a buy in the same instrument dated within
// purchaseClaimWindow of it, and each buy answers for at most one instalment —
// without that, one recorded purchase would satisfy every instalment whose
// window it falls in, and a monthly plan recorded once would look fully kept
// all year. It is the same rule, for the same reason, as the dividend prompt.
//
// Only buys claim an instalment. A sale in the same window is a different act
// and says nothing about whether the plan ran.
func (d *DB) PendingPurchases(userID string, until time.Time) ([]PendingPurchase, error) {
	plans, err := d.ListPlans(userID)
	if err != nil {
		return nil, err
	}
	if len(plans) == 0 {
		return []PendingPurchase{}, nil
	}

	pending := []PendingPurchase{}
	for _, plan := range plans {
		dates, err := dueDates(plan.RecurringPlan, until)
		if err != nil {
			return nil, err
		}
		if len(dates) == 0 {
			continue
		}

		buys := []time.Time{}
		err = d.db.Model(&models.Transaction{}).
			Where("user_id = ? AND instrument_id = ? AND side = ?",
				userID, plan.InstrumentID, models.SideBuy).
			Order("traded_at asc").
			Pluck("traded_at", &buys).Error
		if err != nil {
			return nil, err
		}
		claimed := make([]bool, len(buys))

		for _, date := range dates {
			if claimPurchase(buys, claimed, date) {
				continue
			}
			item := PendingPurchase{
				PlanID:       plan.ID,
				InstrumentID: plan.InstrumentID,
				Symbol:       plan.Symbol,
				Name:         plan.Name,
				Currency:     plan.Currency,
				DueOn:        date,
				Amount:       plan.Amount,
			}
			if price, ok := d.closeOn(plan.InstrumentID, date); ok && price > 0 {
				shares := models.SharesFor(plan.Amount, price)
				item.EstimatedPrice = &price
				item.EstimatedShares = &shares
			}
			pending = append(pending, item)
		}
	}

	sort.Slice(pending, func(i, j int) bool {
		if pending[i].DueOn != pending[j].DueOn {
			return pending[i].DueOn > pending[j].DueOn
		}
		return pending[i].Symbol < pending[j].Symbol
	})
	return pending, nil
}

// claimPurchase reports whether an unclaimed buy can stand as the record of the
// instalment due on date, marking it used if so.
func claimPurchase(buys []time.Time, claimed []bool, date string) bool {
	due, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return false
	}
	for i, at := range buys {
		if claimed[i] {
			continue
		}
		gap := at.UTC().Sub(due)
		if gap < 0 {
			gap = -gap
		}
		if gap > purchaseClaimWindow {
			continue
		}
		claimed[i] = true
		return true
	}
	return false
}

// closeOn returns the close in effect for an instrument on a date, carrying the
// last one forward across a day the market did not open — the same rule the
// curve follows, since a plan dated on a holiday still executed on the next
// session at a price the reader will recognize.
func (d *DB) closeOn(instrumentID, date string) (int64, bool) {
	var row models.DailyClose
	err := d.db.Where("instrument_id = ? AND date <= ?", instrumentID, date).
		Order("date desc").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || err != nil {
		return 0, false
	}
	return row.Close, true
}
