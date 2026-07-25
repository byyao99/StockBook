package db

import (
	"strings"
	"time"

	"gorm.io/gorm"

	"stockbook/internal/models"
)

// PositionView is a holding joined with its instrument, shaped for the API.
//
// MarketValue and UnrealizedPL are derived, never stored: storing them would
// mean rewriting every holding of an instrument each time its quote changes,
// for numbers that are trivial to compute on read. Both are nil when the
// instrument has no quote — an unknown market value is reported as unknown
// rather than quietly as zero.
type PositionView struct {
	ID             string     `json:"id"`
	InstrumentID   string     `json:"instrument_id"`
	Symbol         string     `json:"symbol"`
	Name           string     `json:"name"`
	Market         string     `json:"market"`
	Quantity       int        `json:"quantity"`
	CostBasis      int64      `json:"cost_basis"`
	RealizedPL     int64      `json:"realized_pl"`
	LastPrice      *int64     `json:"last_price"`
	PriceUpdatedAt *time.Time `json:"price_updated_at"`
	MarketValue    *int64     `json:"market_value"`
	UnrealizedPL   *int64     `json:"unrealized_pl"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// positionSortColumns maps the sort keys clients may use to qualified column
// names. The list spans two tables, and `updated_at` exists on both, so the
// qualification is required as well as being the injection guard: a key that is
// not in this map cannot reach SQL at all.
var positionSortColumns = map[string]string{
	"symbol":      "instruments.symbol",
	"name":        "instruments.name",
	"quantity":    "positions.quantity",
	"cost_basis":  "positions.cost_basis",
	"realized_pl": "positions.realized_pl",
	"updated_at":  "positions.updated_at",
}

// positionOrderClause builds a safe ORDER BY fragment for the joined query.
func positionOrderClause(opts ListOptions) string {
	col, ok := positionSortColumns[opts.Sort]
	if !ok {
		col = "instruments.symbol"
	}
	dir := "asc"
	switch strings.ToLower(opts.Order) {
	case "asc":
		dir = "asc"
	case "desc":
		dir = "desc"
	}
	return col + " " + dir
}

// positionQuery is the shared join between a user's positions and the
// instruments they refer to. includeClosed keeps fully-exited holdings, which
// carry no shares but still carry their realized profit or loss.
func (d *DB) positionQuery(userID string, includeClosed bool) *gorm.DB {
	q := d.db.Model(&models.Position{}).
		Joins("JOIN instruments ON instruments.id = positions.instrument_id").
		Where("positions.user_id = ?", userID)
	if !includeClosed {
		q = q.Where("positions.quantity > 0")
	}
	return q
}

// positionRow is the flat scan target for the joined select.
type positionRow struct {
	ID             string
	InstrumentID   string
	Symbol         string
	Name           string
	Market         string
	Quantity       int
	CostBasis      int64
	RealizedPL     int64
	LastPrice      *int64
	PriceUpdatedAt *time.Time
	UpdatedAt      time.Time
}

const positionSelect = `positions.id AS id,
	positions.instrument_id AS instrument_id,
	positions.quantity AS quantity,
	positions.cost_basis AS cost_basis,
	positions.realized_pl AS realized_pl,
	positions.updated_at AS updated_at,
	instruments.symbol AS symbol,
	instruments.name AS name,
	instruments.market AS market,
	instruments.last_price AS last_price,
	instruments.price_updated_at AS price_updated_at`

// ListPositions returns a page of userID's holdings joined with their
// instruments, plus the total count. Closed holdings are excluded unless
// includeClosed is set. Defaults to symbol order.
func (d *DB) ListPositions(userID string, includeClosed bool, opts ListOptions) ([]PositionView, int64, error) {
	var total int64
	if err := d.positionQuery(userID, includeClosed).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	rows := []positionRow{}
	q := d.positionQuery(userID, includeClosed).
		Select(positionSelect).
		Order(positionOrderClause(opts)).
		Offset(opts.Offset)
	if opts.Limit > 0 {
		q = q.Limit(opts.Limit)
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, 0, err
	}

	views := make([]PositionView, 0, len(rows))
	for _, r := range rows {
		views = append(views, r.toView())
	}
	return views, total, nil
}

// toView derives the valuation fields, leaving them nil when there is no quote.
func (r positionRow) toView() PositionView {
	v := PositionView{
		ID:             r.ID,
		InstrumentID:   r.InstrumentID,
		Symbol:         r.Symbol,
		Name:           r.Name,
		Market:         r.Market,
		Quantity:       r.Quantity,
		CostBasis:      r.CostBasis,
		RealizedPL:     r.RealizedPL,
		LastPrice:      r.LastPrice,
		PriceUpdatedAt: r.PriceUpdatedAt,
		UpdatedAt:      r.UpdatedAt,
	}
	if r.LastPrice != nil {
		marketValue := int64(r.Quantity) * *r.LastPrice
		unrealized := marketValue - r.CostBasis
		v.MarketValue = &marketValue
		v.UnrealizedPL = &unrealized
	}
	return v
}

// PortfolioSummary totals a user's book.
//
// TotalMarketValue and TotalUnrealizedPL cover only the holdings that have a
// quote, so PricedCostBasis is reported alongside them and the identity
// TotalUnrealizedPL == TotalMarketValue - PricedCostBasis always holds.
// Comparing PricedPositions against OpenPositions tells a caller how much of the
// book those totals actually account for, which is more honest than folding
// unpriced holdings in as zero.
//
// TotalRealizedPL spans every holding including closed ones — profit already
// banked does not stop counting because the shares are gone.
type PortfolioSummary struct {
	OpenPositions     int   `json:"open_positions"`
	PricedPositions   int   `json:"priced_positions"`
	TotalCostBasis    int64 `json:"total_cost_basis"`
	PricedCostBasis   int64 `json:"priced_cost_basis"`
	TotalMarketValue  int64 `json:"total_market_value"`
	TotalUnrealizedPL int64 `json:"total_unrealized_pl"`
	TotalRealizedPL   int64 `json:"total_realized_pl"`
}

// PortfolioSummary computes the totals across userID's whole book.
func (d *DB) PortfolioSummary(userID string) (PortfolioSummary, error) {
	rows := []positionRow{}
	if err := d.positionQuery(userID, true).Select(positionSelect).Scan(&rows).Error; err != nil {
		return PortfolioSummary{}, err
	}

	var s PortfolioSummary
	for _, r := range rows {
		s.TotalRealizedPL += r.RealizedPL
		if r.Quantity == 0 {
			continue
		}
		s.OpenPositions++
		s.TotalCostBasis += r.CostBasis
		if r.LastPrice != nil {
			s.PricedPositions++
			s.PricedCostBasis += r.CostBasis
			s.TotalMarketValue += int64(r.Quantity) * *r.LastPrice
		}
	}
	s.TotalUnrealizedPL = s.TotalMarketValue - s.PricedCostBasis
	return s, nil
}

// GetPosition returns one of userID's holdings by instrument, or ErrNotFound.
func (d *DB) GetPosition(userID, instrumentID string) (PositionView, error) {
	var r positionRow
	err := d.positionQuery(userID, true).
		Select(positionSelect).
		Where("positions.instrument_id = ?", instrumentID).
		Limit(1).Scan(&r).Error
	if err != nil {
		return PositionView{}, err
	}
	if r.ID == "" {
		return PositionView{}, ErrNotFound
	}
	return r.toView(), nil
}
