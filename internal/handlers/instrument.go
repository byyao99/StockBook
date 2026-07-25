package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"stockbook/internal/db"
	"stockbook/internal/models"
)

// InstrumentHandler handles the instrument master data.
type InstrumentHandler struct {
	db *db.DB
}

// NewInstrumentHandler creates an InstrumentHandler.
func NewInstrumentHandler(s *db.DB) *InstrumentHandler {
	return &InstrumentHandler{db: s}
}

// instrumentRequest is the payload for creating or replacing an instrument. The
// quote is not settable here — see SetPrice.
type instrumentRequest struct {
	Symbol string `json:"symbol" binding:"required,max=20"`
	Name   string `json:"name" binding:"required,max=120"`
	Market string `json:"market" binding:"required"`
}

// normalize trims the fields and folds the symbol and market to their canonical
// spellings, so that "  2330 " and "twse" match an existing 2330/TWSE rather
// than creating a near-duplicate.
func (r *instrumentRequest) normalize() error {
	r.Symbol = strings.ToUpper(strings.TrimSpace(r.Symbol))
	r.Name = strings.TrimSpace(r.Name)
	if r.Symbol == "" {
		return errEmptySymbol
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
		ID:     uuid.NewString(),
		Symbol: req.Symbol,
		Name:   req.Name,
		Market: req.Market,
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
		Symbol: req.Symbol,
		Name:   req.Name,
		Market: req.Market,
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

	item, err := h.db.UpdateInstrumentPrice(c.Param("id"), req.LastPrice)
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
