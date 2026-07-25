package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"stockbook/internal/db"
	"stockbook/internal/middleware"
	"stockbook/internal/models"
	"stockbook/internal/quotes"
)

// QuoteFetcher is the slice of a quote provider this handler needs. Keeping it
// an interface lets the tests drive the refresh endpoint without a network call,
// and makes swapping providers a one-file change.
type QuoteFetcher interface {
	Fetch(ctx context.Context, ticker string) (quotes.Quote, error)
}

// InstrumentHandler handles the instrument master data.
type InstrumentHandler struct {
	db     *db.DB
	quotes QuoteFetcher
}

// NewInstrumentHandler creates an InstrumentHandler. quotes may be nil, in which
// case the refresh endpoint reports that quote fetching is not configured.
func NewInstrumentHandler(s *db.DB, q QuoteFetcher) *InstrumentHandler {
	return &InstrumentHandler{db: s, quotes: q}
}

// instrumentRequest is the payload for creating or replacing an instrument. The
// quote is not settable here — see SetPrice.
type instrumentRequest struct {
	Symbol string `json:"symbol" binding:"required,max=20"`
	Name   string `json:"name" binding:"required,max=120"`
	Market string `json:"market" binding:"required"`
	// Currency is optional; an omitted one defaults to whatever the market
	// normally trades in, which is right for the overwhelming majority of cases
	// and still overridable for the rest.
	Currency string `json:"currency"`
}

// normalize trims the fields and folds the symbol, market and currency to their
// canonical spellings, so that "  2330 " and "twse" match an existing 2330/TWSE
// rather than creating a near-duplicate.
func (r *instrumentRequest) normalize() error {
	r.Symbol = strings.ToUpper(strings.TrimSpace(r.Symbol))
	r.Name = strings.TrimSpace(r.Name)
	if r.Symbol == "" {
		return errEmptySymbol
	}
	// A symbol carrying an exchange suffix is a provider ticker pasted whole.
	// The suffix is derived from the market, so keeping it would produce
	// "2330.TW.TW", and the only symptom would be a quote that never arrives —
	// long after the mistake was made.
	if suffix, hasSuffix := quotes.ExchangeSuffix(r.Symbol); hasSuffix {
		return &validationError{fmt.Sprintf(
			"symbol should not include the %s exchange suffix; enter %q and select the market instead",
			suffix, strings.TrimSuffix(r.Symbol, suffix))}
	}
	market, ok := models.CanonicalMarket(strings.TrimSpace(r.Market))
	if !ok {
		return errUnknownMarket
	}
	r.Market = market

	if strings.TrimSpace(r.Currency) == "" {
		r.Currency = string(models.DefaultCurrencyForMarket(market))
		return nil
	}
	currency, ok := models.CanonicalCurrency(strings.TrimSpace(r.Currency))
	if !ok {
		return errUnknownCurrency
	}
	r.Currency = string(currency)
	return nil
}

var (
	errEmptySymbol     = &validationError{"symbol must not be blank"}
	errUnknownMarket   = &validationError{"market must be one of: " + strings.Join(models.Markets, ", ")}
	errUnknownCurrency = &validationError{"currency must be TWD or USD"}
)

// validationError is a request-level problem that always maps to 400.
type validationError struct{ msg string }

func (e *validationError) Error() string { return e.msg }

// Create handles POST /api/v1/instruments (admin only).
func (h *InstrumentHandler) Create(c *gin.Context) {
	var req instrumentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := req.normalize(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	item, err := h.db.CreateInstrument(models.Instrument{
		ID:       uuid.NewString(),
		Symbol:   req.Symbol,
		Name:     req.Name,
		Market:   req.Market,
		Currency: models.Currency(req.Currency),
	})
	if err != nil {
		respondDBError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": item})
}

// List handles GET /api/v1/instruments. Optional ?market filters exactly and ?q
// matches a substring of the symbol or the name.
func (h *InstrumentHandler) List(c *gin.Context) {
	opts := parseListOptions(c)
	filter := db.InstrumentFilter{Q: strings.TrimSpace(c.Query("q"))}
	if market := c.Query("market"); market != "" {
		canonical, ok := models.CanonicalMarket(market)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": errUnknownMarket.Error()})
			return
		}
		filter.Market = canonical
	}

	items, total, err := h.db.ListInstruments(opts, filter)
	if err != nil {
		respondDBError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"data":       items,
		"pagination": paginationMeta(opts, total),
	})
}

// Get handles GET /api/v1/instruments/:id.
func (h *InstrumentHandler) Get(c *gin.Context) {
	item, err := h.db.GetInstrument(c.Param("id"))
	if err != nil {
		respondDBError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": item})
}

// Update handles PUT /api/v1/instruments/:id (admin only). Renaming an
// instrument does not rewrite history: existing transactions keep the symbol
// they were entered with.
func (h *InstrumentHandler) Update(c *gin.Context) {
	var req instrumentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := req.normalize(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	item, err := h.db.UpdateInstrument(c.Param("id"), models.Instrument{
		Symbol:   req.Symbol,
		Name:     req.Name,
		Market:   req.Market,
		Currency: models.Currency(req.Currency),
	})
	if err != nil {
		respondDBError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": item})
}

// setPriceRequest carries a quote in cents. A nil LastPrice explicitly clears
// the quote, which is why the field is a pointer: an omitted price and a price
// of zero must not mean the same thing.
type setPriceRequest struct {
	LastPrice *int64 `json:"last_price"`
}

// SetPrice handles PATCH /api/v1/instruments/:id/price (admin only). It is split
// out from Update because maintaining quotes is routine daily work while editing
// the master data is not — they deserve to be separately grantable and
// separately auditable.
func (h *InstrumentHandler) SetPrice(c *gin.Context) {
	var req setPriceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.LastPrice != nil && *req.LastPrice < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "last_price must not be negative"})
		return
	}

	item, err := h.db.UpdateInstrumentPrice(c.Param("id"), db.QuoteUpdate{Price: req.LastPrice})
	if err != nil {
		respondDBError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": item})
}

// Delete handles DELETE /api/v1/instruments/:id (admin only). It fails with 409
// when transactions reference the instrument.
func (h *InstrumentHandler) Delete(c *gin.Context) {
	if err := h.db.DeleteInstrument(c.Param("id")); err != nil {
		respondDBError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// refreshResult reports what happened to one instrument during a quote refresh.
// Error carries the provider's own wording when there is any, because a
// flattened "fetch failed" tells an operator nothing about which symbol is wrong
// or why.
type refreshResult struct {
	InstrumentID string `json:"instrument_id"`
	Symbol       string `json:"symbol"`
	// Ticker is the symbol the provider was actually asked for, which is
	// derived from the instrument's market. Reporting it makes a mis-filed
	// market self-evident: 2330 on TPEX is looked up as 2330.TWO and finds
	// nothing, because it trades on TWSE as 2330.TW.
	Ticker    string `json:"ticker,omitempty"`
	Status    string `json:"status"` // updated | skipped | failed
	LastPrice *int64 `json:"last_price,omitempty"`
	Error     string `json:"error,omitempty"`
}

const (
	// refreshTimeout bounds a whole refresh run, however many instruments it covers.
	refreshTimeout = 60 * time.Second

	// quoteFreshness is how recent a quote has to be for a refresh to leave it
	// alone. Every fetch is an outbound call to a third party, and this endpoint
	// is open to any signed-in user, so repeatedly pressing refresh must not turn
	// into repeated traffic. For a ledger of trades that already happened, a
	// price this old is current enough.
	quoteFreshness = 15 * time.Minute
)

// RefreshQuotes handles POST /api/v1/instruments/refresh-quotes for any
// authenticated user. It fetches a current price for every instrument whose
// quote has gone stale and reports the outcome per instrument.
//
// It is open to plain users, not just admins, because the holdings page is
// built on unrealized profit and loss and that is dead without a current price:
// gating quotes behind an admin would leave every other user unable to do
// anything about a stale book. Quotes are shared, objective market data rather
// than anything the caller owns, so refreshing them is not a privileged act.
//
// Two things keep that from becoming a way to hammer the provider: instruments
// quoted within quoteFreshness are left alone entirely, so pressing refresh
// twice costs one round of traffic, and the route itself is rate-limited per
// client IP.
//
// A partial failure is not an error for the run as a whole: one delisted ticker
// must not stop the other twenty from updating. The response is 200 with each
// instrument's own status, and the caller decides what to make of the failures.
// An instrument that cannot be fetched keeps whatever quote it already had —
// never a zero, and never a silently refreshed timestamp, so the staleness shown
// in the UI stays truthful.
func (h *InstrumentHandler) RefreshQuotes(c *gin.Context) {
	if h.quotes == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "quote fetching is not configured"})
		return
	}

	// A large page: this walks the whole master data, which is small by nature.
	items, _, err := h.db.ListInstruments(db.ListOptions{Limit: maxLimit}, db.InstrumentFilter{})
	if err != nil {
		respondDBError(c, err)
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), refreshTimeout)
	defer cancel()

	// Results cover only the instruments worth reporting on. Ones already
	// current are counted but left out: on a repeat press they would be the
	// whole list, and none of them is something the caller can act on.
	results := make([]refreshResult, 0, len(items))
	updated, failed, fresh := 0, 0, 0
	for _, item := range items {
		if isQuoteFresh(item) {
			fresh++
			continue
		}
		result := h.refreshOne(ctx, item)
		switch result.Status {
		case "updated":
			updated++
		case "failed":
			failed++
		}
		results = append(results, result)
	}

	slog.Info("quotes refreshed",
		slog.String(middleware.RequestIDKey, middleware.RequestIDFromContext(c)),
		slog.String("actor_id", callerID(c)),
		slog.Int("updated", updated),
		slog.Int("failed", failed),
		slog.Int("fresh", fresh),
		slog.Int("total", len(items)),
	)

	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"updated": updated,
		"failed":  failed,
		"fresh":   fresh,
		"results": results,
	}})
}

// isQuoteFresh reports whether the provider was asked recently enough that
// asking again is not worth the call.
//
// This deliberately reads QuoteCheckedAt rather than PriceUpdatedAt. The latter
// is the market time the price was good for, which for a closed market is hours
// or days old however recently it was fetched — using it here would mean every
// refresh outside trading hours re-requested everything.
//
// An instrument with no quote at all is never fresh: that is exactly the case a
// refresh exists to fix.
func isQuoteFresh(item models.Instrument) bool {
	if item.LastPrice == nil || item.QuoteCheckedAt == nil {
		return false
	}
	return time.Since(*item.QuoteCheckedAt) < quoteFreshness
}

// refreshOne fetches and stores a single instrument's quote, converting every
// failure into a reportable result rather than aborting the run.
func (h *InstrumentHandler) refreshOne(ctx context.Context, item models.Instrument) refreshResult {
	result := refreshResult{InstrumentID: item.ID, Symbol: item.Symbol}

	ticker, ok := quotes.Ticker(item.Symbol, item.Market)
	if !ok {
		result.Status = "skipped"
		result.Error = "no quote source for market " + item.Market
		return result
	}
	result.Ticker = ticker

	quote, err := h.quotes.Fetch(ctx, ticker)
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}

	// A currency that disagrees with the instrument's own is a data problem, not
	// a price to store: the cost basis already recorded against this instrument
	// is denominated in the currency on file, so adopting the provider's would
	// reinterpret history rather than correct it. Report it and change nothing.
	if item.Currency != "" && quote.Currency != item.Currency {
		result.Status = "failed"
		result.Error = fmt.Sprintf("provider quotes %s in %s but the instrument is recorded in %s",
			item.Symbol, quote.Currency, item.Currency)
		return result
	}
	if item.Currency == "" {
		if err := h.db.SetInstrumentCurrency(item.ID, quote.Currency); err != nil &&
			!errors.Is(err, db.ErrCurrencyLocked) {
			result.Status = "failed"
			result.Error = err.Error()
			return result
		}
	}

	price := quote.Price
	asOf := quote.AsOf
	if _, err := h.db.UpdateInstrumentPrice(item.ID, db.QuoteUpdate{Price: &price, AsOf: &asOf}); err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}

	result.Status = "updated"
	result.LastPrice = &price
	return result
}
