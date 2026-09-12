package db

import (
	"sort"
	"time"

	"gorm.io/gorm/clause"

	"stockbook/internal/models"
)

// dividendClaimWindow is how long after an ex-date a ledger entry may be dated
// and still be taken as the record of that distribution.
//
// The two dates are genuinely different events: the ex-date decides who is owed,
// and the cash lands weeks later, which is when a user records it. Sixty days
// comfortably covers that lag in both markets this system addresses while
// staying inside a quarterly cycle — TSMC's ex-dates run about 86 days apart, so
// a wider window would let one recorded entry answer for two distributions and
// silently hide the second.
const dividendClaimWindow = 60 * 24 * time.Hour

// PendingDividend is a distribution the book was entitled to and has no ledger
// entry for.
//
// It is a prompt, never a posting. Shares is what was actually held on the
// ex-date, and Estimated is that times the amount per share — before whatever
// was withheld, which this system deliberately does not try to guess. The user
// enters what actually arrived.
type PendingDividend struct {
	InstrumentID string          `json:"instrument_id"`
	Symbol       string          `json:"symbol"`
	Name         string          `json:"name"`
	Currency     models.Currency `json:"currency"`
	ExDate       string          `json:"ex_date"`
	PerShare     int64           `json:"per_share"`
	Shares       int             `json:"shares"`
	Estimated    int64           `json:"estimated"`
}

// SaveDividendEvents records the distributions a provider reported for one
// instrument, replacing any already held for the same ex-date.
//
// The upsert is what makes a refetch safe. A sync window overlaps what is
// already stored on purpose, and a provider does revise an amount, so inserting
// blindly would either fail on the key or leave two rows for one distribution
// where every reader expects one.
func (d *DB) SaveDividendEvents(instrumentID string, events []models.DividendEvent) error {
	if len(events) == 0 {
		return nil
	}
	rows := make([]models.DividendEvent, 0, len(events))
	for _, e := range events {
		e.InstrumentID = instrumentID
		rows = append(rows, e)
	}
	return d.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "instrument_id"}, {Name: "ex_date"}},
		DoUpdates: clause.AssignmentColumns([]string{"amount", "updated_at"}),
	}).CreateInBatches(rows, 500).Error
}

// dividendLedgerRow is the flat scan target for the entitlement replay.
type dividendLedgerRow struct {
	InstrumentID string
	Symbol       string
	Name         string
	Currency     models.Currency
	Side         models.TransactionSide
	Quantity     int
	TradedAt     time.Time
}

// PendingDividends reports distributions userID was entitled to and has not
// recorded, newest ex-date first.
//
// Entitlement is decided by replaying the ledger to the ex-date, which is the
// only way to know it: a holding sold last month was still owed the payout from
// the month before, and the current position says nothing about that. This is
// the same reason a dividend entry is never checked against shares on hand.
//
// An unrecorded distribution is one with no dividend entry for that instrument
// dated within dividendClaimWindow after it. Each entry answers for at most one
// distribution, claimed by the earliest it could belong to, so a quarterly payer
// recorded once does not silently satisfy two quarters.
//
// The failure this is pointed at is the common one: a dividend arrives weeks
// after anyone was thinking about the stock, and simply never gets written down.
// Reporting it as a prompt rather than posting it keeps the ledger what it is —
// a record of what the user says happened.
func (d *DB) PendingDividends(userID string) ([]PendingDividend, error) {
	entries := []dividendLedgerRow{}
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
		return []PendingDividend{}, nil
	}

	held := map[string][]dividendLedgerRow{}
	for _, e := range entries {
		held[e.InstrumentID] = append(held[e.InstrumentID], e)
	}

	ids := make([]string, 0, len(held))
	for id := range held {
		ids = append(ids, id)
	}
	events := []models.DividendEvent{}
	if err := d.db.Where("instrument_id IN ?", ids).
		Order("ex_date asc").Find(&events).Error; err != nil {
		return nil, err
	}

	// Which recorded dividend entries are still unspoken for, per instrument.
	// Events are walked oldest first, so the earliest distribution an entry
	// could belong to is the one that claims it.
	claimed := map[string]*dividendClaims{}
	for id, ledger := range held {
		claimed[id] = newDividendClaims(ledger)
	}

	pending := []PendingDividend{}
	for _, event := range events {
		ledger := held[event.InstrumentID]
		shares := sharesOn(ledger, event.ExDate)
		if shares <= 0 {
			continue
		}
		if claimed[event.InstrumentID].claim(event.ExDate) {
			continue
		}
		pending = append(pending, PendingDividend{
			InstrumentID: event.InstrumentID,
			Symbol:       ledger[0].Symbol,
			Name:         ledger[0].Name,
			Currency:     ledger[0].Currency,
			ExDate:       event.ExDate,
			PerShare:     event.Amount,
			Shares:       shares,
			Estimated:    int64(shares) * event.Amount,
		})
	}

	// Newest first: a distribution from last month is the one still worth
	// chasing, and an old one the user has decided to ignore sinks.
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].ExDate != pending[j].ExDate {
			return pending[i].ExDate > pending[j].ExDate
		}
		return pending[i].Symbol < pending[j].Symbol
	})
	return pending, nil
}

// sharesOn reports how many shares the ledger leaves held at the close of date.
// A dividend entry moves no shares, so only buys and sells count.
func sharesOn(ledger []dividendLedgerRow, date string) int {
	shares := 0
	for _, e := range ledger {
		if e.TradedAt.UTC().Format(time.DateOnly) > date {
			break // the ledger is in order
		}
		switch e.Side {
		case models.SideBuy:
			shares += e.Quantity
		case models.SideSell:
			shares -= e.Quantity
		}
	}
	return shares
}

// dividendClaims tracks which of one instrument's recorded dividend entries have
// already been taken as the record of a distribution.
//
// Each entry can answer for at most one. Without that, a single recorded
// dividend would satisfy every distribution whose window it happens to fall in,
// and a quarterly payer recorded once would look fully recorded all year — the
// exact failure this report exists to catch.
type dividendClaims struct {
	dates []time.Time
	taken []bool
}

func newDividendClaims(ledger []dividendLedgerRow) *dividendClaims {
	c := &dividendClaims{}
	for _, e := range ledger {
		if e.Side != models.SideDividend {
			continue
		}
		c.dates = append(c.dates, e.TradedAt.UTC())
	}
	c.taken = make([]bool, len(c.dates))
	return c
}

// claim reports whether an unclaimed entry can stand as the record of the
// distribution with this ex-date, and marks it used if so.
func (c *dividendClaims) claim(exDate string) bool {
	ex, err := time.Parse(time.DateOnly, exDate)
	if err != nil {
		return false
	}
	for i, at := range c.dates {
		if c.taken[i] {
			continue
		}
		gap := at.Sub(ex)
		if gap < 0 || gap > dividendClaimWindow {
			continue
		}
		c.taken[i] = true
		return true
	}
	return false
}
