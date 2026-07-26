package handlers

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"stockbook/internal/db"
	"stockbook/internal/middleware"
	"stockbook/internal/models"
)

// TransactionHandler handles a user's ledger.
type TransactionHandler struct {
	db *db.DB
}

// NewTransactionHandler creates a TransactionHandler.
func NewTransactionHandler(s *db.DB) *TransactionHandler {
	return &TransactionHandler{db: s}
}

// invalidSideMessage names every kind of entry a ledger accepts, so a caller who
// gets it wrong is told what the alternatives are rather than just that this one
// was refused.
var invalidSideMessage = "side must be one of: " + joinSides(models.Sides)

func joinSides(sides []models.TransactionSide) string {
	names := make([]string, 0, len(sides))
	for _, s := range sides {
		names = append(names, string(s))
	}
	return strings.Join(names, ", ")
}

// transactionRequest is the payload for entering or editing a trade.
//
// There is deliberately no net_amount field: the cash movement is the server's
// arithmetic over quantity, price and fee, so a client cannot assert a total
// that disagrees with its own line items. TradedAt is a pointer so an omitted
// timestamp can default to now while an explicit zero time stays an error.
type transactionRequest struct {
	InstrumentID string                 `json:"instrument_id" binding:"required"`
	Side         models.TransactionSide `json:"side" binding:"required"`
	Quantity     int                    `json:"quantity" binding:"required,gt=0"`
	Price        int64                  `json:"price" binding:"required,gte=0"`
	Fee          int64                  `json:"fee" binding:"omitempty,gte=0"`
	TradedAt     *time.Time             `json:"traded_at"`
	Note         string                 `json:"note" binding:"max=500"`
}

// resolveTradedAt defaults an omitted timestamp to now and rejects future
// trades — this is a ledger of things that already happened, and a future entry
// would sort after every real one and quietly distort the running average.
func resolveTradedAt(t *time.Time) (time.Time, error) {
	if t == nil {
		return time.Now(), nil
	}
	if t.After(time.Now()) {
		return time.Time{}, &validationError{"traded_at must not be in the future"}
	}
	return *t, nil
}

// Create handles POST /api/v1/transactions for the authenticated user.
func (h *TransactionHandler) Create(c *gin.Context) {
	var req transactionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !req.Side.Valid() {
		c.JSON(http.StatusBadRequest, gin.H{"error": invalidSideMessage})
		return
	}
	tradedAt, err := resolveTradedAt(req.TradedAt)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	created, err := h.db.CreateTransaction(models.Transaction{
		ID:           uuid.NewString(),
		UserID:       callerID(c),
		InstrumentID: req.InstrumentID,
		Side:         req.Side,
		Quantity:     req.Quantity,
		Price:        req.Price,
		Fee:          req.Fee,
		TradedAt:     tradedAt,
		Note:         req.Note,
	})
	if err != nil {
		respondDBError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": created})
}

// List handles GET /api/v1/transactions, always scoped to the caller's own
// ledger. Optional ?instrument_id, ?side, ?from and ?to narrow it.
func (h *TransactionHandler) List(c *gin.Context) {
	opts := parseListOptions(c)
	filter := db.TransactionFilter{
		UserID:       callerID(c),
		InstrumentID: c.Query("instrument_id"),
	}

	if side := c.Query("side"); side != "" {
		s := models.TransactionSide(side)
		if !s.Valid() {
			c.JSON(http.StatusBadRequest, gin.H{"error": invalidSideMessage})
			return
		}
		filter.Side = s
	}
	from, to, err := parseDateRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	filter.From, filter.To = from, to

	items, total, err := h.db.ListTransactions(opts, filter)
	if err != nil {
		respondDBError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"data":       items,
		"pagination": paginationMeta(opts, total),
	})
}

// parseDateRange reads the ?from and ?to bounds shared by the ledger list and
// the reports. Either may be absent, which leaves that end open.
//
// A bare `to=YYYY-MM-DD` names the whole of that day, so it is stretched to the
// last instant of it. A plain `traded_at <= 2026-12-31` would mean midnight and
// silently drop every trade made on the closing day of the range — the last
// entry of a yearly report being the one it omits.
//
// Bare dates are read as UTC, which is the same calendar the rest of the system
// speaks: the trade form stores a chosen date at noon UTC precisely so that the
// day a trade belongs to does not depend on the reader's zone, and the ledger
// displays it by taking the first ten characters of the UTC timestamp.
func parseDateRange(c *gin.Context) (*time.Time, *time.Time, error) {
	from, _, err := parseTimeQuery(c, "from")
	if err != nil {
		return nil, nil, err
	}
	to, dateOnly, err := parseTimeQuery(c, "to")
	if err != nil {
		return nil, nil, err
	}
	if to != nil && dateOnly {
		endOfDay := to.AddDate(0, 0, 1).Add(-time.Nanosecond)
		to = &endOfDay
	}
	return from, to, nil
}

// parseTimeQuery reads an RFC 3339 timestamp or a bare date from query param
// name. A missing param yields a nil time and no error. The second result
// reports whether the value was a bare date, which callers treating a date as a
// whole day need to know.
func parseTimeQuery(c *gin.Context, name string) (*time.Time, bool, error) {
	raw := c.Query(name)
	if raw == "" {
		return nil, false, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return &t, false, nil
	}
	if t, err := time.Parse(time.DateOnly, raw); err == nil {
		return &t, true, nil
	}
	return nil, false, &validationError{name + " must be an RFC 3339 timestamp or a YYYY-MM-DD date"}
}

// Get handles GET /api/v1/transactions/:id. Someone else's transaction reads as
// a 404 rather than a 403, so IDs cannot be probed for existence.
func (h *TransactionHandler) Get(c *gin.Context) {
	t, err := h.db.GetTransaction(c.Param("id"), callerID(c))
	if err != nil {
		respondDBError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": t})
}

// updateTransactionRequest is the payload for editing a ledger entry. Side and
// instrument are absent on purpose: changing either would move the entry to a
// different position, which is clearer as a delete plus a fresh entry.
type updateTransactionRequest struct {
	Quantity int        `json:"quantity" binding:"required,gt=0"`
	Price    int64      `json:"price" binding:"required,gte=0"`
	Fee      int64      `json:"fee" binding:"omitempty,gte=0"`
	TradedAt *time.Time `json:"traded_at"`
	Note     string     `json:"note" binding:"max=500"`
}

// Update handles PUT /api/v1/transactions/:id. The affected position is replayed
// from the ledger afterwards, so an edit that would leave a later sell short of
// shares is rejected with 409 and nothing is written.
func (h *TransactionHandler) Update(c *gin.Context) {
	var req updateTransactionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	tradedAt, err := resolveTradedAt(req.TradedAt)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	updated, err := h.db.UpdateTransaction(c.Param("id"), callerID(c), db.TransactionUpdate{
		Quantity: req.Quantity,
		Price:    req.Price,
		Fee:      req.Fee,
		TradedAt: tradedAt,
		Note:     req.Note,
	})
	if err != nil {
		respondDBError(c, err)
		return
	}
	slog.Info("transaction updated",
		slog.String(middleware.RequestIDKey, middleware.RequestIDFromContext(c)),
		slog.String("actor_id", callerID(c)),
		slog.String("transaction_id", updated.ID),
		slog.String("symbol", updated.Symbol),
	)
	c.JSON(http.StatusOK, gin.H{"data": updated})
}

// Delete handles DELETE /api/v1/transactions/:id. Like Update it replays the
// position afterwards, so removing a buy that a later sell drew on is rejected
// with 409 rather than leaving the ledger in a state its own rules forbid.
func (h *TransactionHandler) Delete(c *gin.Context) {
	if err := h.db.DeleteTransaction(c.Param("id"), callerID(c)); err != nil {
		respondDBError(c, err)
		return
	}
	slog.Info("transaction deleted",
		slog.String(middleware.RequestIDKey, middleware.RequestIDFromContext(c)),
		slog.String("actor_id", callerID(c)),
		slog.String("transaction_id", c.Param("id")),
	)
	c.Status(http.StatusNoContent)
}
