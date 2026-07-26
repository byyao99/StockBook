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

// QuoteProvider is the slice of a market-data provider this handler needs.
// Keeping it an interface lets the tests drive both endpoints without a network
// call, and makes swapping providers a one-file change.
type QuoteProvider interface {
	Fetch(ctx context.Context, ticker string) (quotes.Quote, error)
	Search(ctx context.Context, query string) ([]quotes.SearchResult, error)
}

// InstrumentHandler handles the instrument master data.
type InstrumentHandler struct {
	db     *db.DB
	quotes QuoteProvider
}

// NewInstrumentHandler creates an InstrumentHandler. quotes may be nil, in which
// case the refresh endpoint reports that quote fetching is not configured.
func NewInstrumentHandler(s *db.DB, q QuoteProvider) *InstrumentHandler {
	return &InstrumentHandler{db: s, quotes: q}
}

// createInstrumentRequest names a listing to add. Only the symbol and market
// are accepted: the name, currency and opening price all come from the provider,
// so nothing a caller claims about an instrument can disagree with the source
// that will price it.
type createInstrumentRequest struct {
	Symbol string `json:"symbol" binding:"required,max=20"`
	Market string `json:"market" binding:"required"`
}

// normalize folds the symbol and market to their canonical spellings, so that
// "  2330 " and "twse" match an existing 2330/TWSE rather than creating a
// near-duplicate.
func (r *createInstrumentRequest) normalize() error {
	r.Symbol = strings.ToUpper(strings.TrimSpace(r.Symbol))
	if r.Symbol == "" {
		return errEmptySymbol
	}
	// A symbol carrying an exchange suffix is a provider ticker pasted whole.
	// The suffix is derived from the market, so keeping it would produce
	// "2330.TW.TW" and the lookup below would fail for a confusing reason.
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
	return nil
}

var (
	errEmptySymbol   = &validationError{"symbol must not be blank"}
	errUnknownMarket = &validationError{"market must be one of: " + strings.Join(models.Markets, ", ")}
)

// validationError is a request-level problem that always maps to 400.
type validationError struct{ msg string }

func (e *validationError) Error() string { return e.msg }

// Create handles POST /api/v1/instruments (admin only).
//
// An instrument is only created if the provider can price it: the quote is
// fetched first, and its failure is the request's failure. That makes an
// unquotable instrument unrepresentable rather than merely discouraged — the
// state that produced every quote problem so far (a symbol the provider has
// never heard of, sitting in the master data looking normal) can no longer
// exist. It also means a new instrument arrives already priced, so nothing has
// to be typed in by hand afterwards.
//
// The name and currency are taken from the provider rather than the caller, for
// the same reason transaction amounts are computed server-side: the authority
// on what an instrument is called and what it trades in is the source that
// quotes it.
func (h *InstrumentHandler) Create(c *gin.Context) {
	if h.quotes == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "quote lookup is not configured"})
		return
	}
	var req createInstrumentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := req.normalize(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ticker, ok := quotes.Ticker(req.Symbol, req.Market)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "no quote source for market " + req.Market +
				"; only markets with a price feed can be tracked",
		})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), searchTimeout)
	defer cancel()

	quote, err := h.quotes.Fetch(ctx, ticker)
	if err != nil {
		// Separate "the provider does not know this symbol", which the caller
		// can fix by choosing a different one, from "the provider is unwell",
		// which they can only wait out.
		if errors.Is(err, quotes.ErrNoQuote) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	// An index, a futures contract or a currency pair prices perfectly well and
	// still cannot be owned as shares. That used to be refused only incidentally,
	// because such things trade on venues the market mapping did not list; now
	// that a US listing is accepted on whatever venue carries it, holdability is
	// asked about directly.
	if !quotes.IsHoldable(quote.Type) {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": fmt.Sprintf(
			"%s is a %s and cannot be held as shares", ticker, strings.ToLower(quote.Type))})
		return
	}
	// Cross-check the listing actually reached: a symbol paired with the wrong
	// market can still resolve to some other exchange's instrument, and adopting
	// that silently would file a foreign listing under a local market.
	if _, market, ok := quotes.FromTicker(ticker, quote.Exchange); !ok || market != req.Market {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": fmt.Sprintf(
			"%s resolves to a %s listing, not %s", ticker, quote.Exchange, req.Market)})
		return
	}
	// A US listing prices in dollars and a Taiwanese one in NT dollars. This is
	// the check that catches a foreign listing the provider named without a
	// suffix — the case the cross-check above cannot see, because it reads a
	// bare ticker as a US one by design.
	if expected := models.DefaultCurrencyForMarket(req.Market); quote.Currency != expected {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": fmt.Sprintf(
			"%s is quoted in %s, which is not what a %s listing trades in",
			ticker, quote.Currency, req.Market)})
		return
	}

	name := quote.Name
	if name == "" {
		name = req.Symbol
	}
	now := time.Now()
	item, err := h.db.CreateInstrument(models.Instrument{
		ID:             uuid.NewString(),
		Symbol:         req.Symbol,
		Name:           name,
		Market:         req.Market,
		Currency:       quote.Currency,
		LastPrice:      &quote.Price,
		PriceUpdatedAt: &quote.AsOf,
		QuoteCheckedAt: &now,
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

// renameInstrumentRequest carries the one thing about an instrument that is
// this system's to decide.
type renameInstrumentRequest struct {
	Name string `json:"name" binding:"required,max=120"`
}

// Update handles PUT /api/v1/instruments/:id (admin only) and renames an
// instrument.
//
// Only the display name is editable. The symbol and market are its identity and
// determine how it is priced — editing them is what filed 2330 under TPEX and
// left it unquotable — while the currency belongs to the provider and is baked
// into every cost basis recorded since. A listing entered wrongly is deleted and
// re-added, which the ledger guard already prevents once trades exist against it.
//
// Renaming does not rewrite history: existing transactions keep the symbol they
// were entered with.
func (h *InstrumentHandler) Update(c *gin.Context) {
	var req renameInstrumentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name must not be blank"})
		return
	}

	item, err := h.db.RenameInstrument(c.Param("id"), name)
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

// searchCandidate is one instrument the provider knows about, shaped for the
// admin's pick-and-add flow. Symbol, Market and Currency are already the values
// that would be stored, so adding one is a single click with nothing typed —
// and nothing that can be mistyped.
//
// Exists reports whether the master data already holds this symbol, so a
// duplicate is visible before it is attempted rather than as a 409 afterwards.
type searchCandidate struct {
	Symbol   string          `json:"symbol"`
	Name     string          `json:"name"`
	Market   string          `json:"market"`
	Currency models.Currency `json:"currency"`
	Ticker   string          `json:"ticker"`
	Exists   bool            `json:"exists"`
}

// searchTimeout bounds a single lookup against the provider.
const searchTimeout = 10 * time.Second

// Search handles GET /api/v1/instruments/search?q= (admin only). It asks the
// provider for instruments matching a symbol or name and returns the ones this
// system can model, pre-translated into the symbol/market/currency that would be
// stored.
//
// This exists because typing instruments in by hand is both tedious and the
// main source of unquotable entries: a symbol invented at the keyboard has no
// guarantee the provider knows it, and the mistake only surfaces later as a
// quote that never arrives. A symbol chosen from the provider's own answer is
// quotable by construction.
//
// The provider matches on symbols and English names; a query in Chinese returns
// nothing, which is a limitation of the source rather than of this endpoint.
func (h *InstrumentHandler) Search(c *gin.Context) {
	if h.quotes == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "quote lookup is not configured"})
		return
	}
	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "q must not be blank"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), searchTimeout)
	defer cancel()

	found, err := h.quotes.Search(ctx, query)
	if err != nil {
		// The provider's own wording again: a caller can act on "returned 429"
		// in a way they cannot act on "search failed".
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	candidates := make([]searchCandidate, 0, len(found))
	for _, r := range found {
		_, err := h.db.GetInstrumentBySymbol(r.Symbol)
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			respondDBError(c, err)
			return
		}
		candidates = append(candidates, searchCandidate{
			Symbol:   r.Symbol,
			Name:     r.Name,
			Market:   r.Market,
			Currency: r.Currency,
			Ticker:   r.Ticker,
			Exists:   err == nil,
		})
	}
	c.JSON(http.StatusOK, gin.H{"data": candidates})
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
