package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"stockbook/internal/db"
)

// PositionHandler serves a user's holdings and portfolio totals.
type PositionHandler struct {
	db *db.DB
}

// NewPositionHandler creates a PositionHandler.
func NewPositionHandler(s *db.DB) *PositionHandler {
	return &PositionHandler{db: s}
}

// List handles GET /api/v1/positions, always scoped to the caller. Fully-exited
// holdings are hidden unless ?include_closed=true; they hold no shares but do
// keep the profit or loss banked on the way out.
func (h *PositionHandler) List(c *gin.Context) {
	opts := parseListOptions(c)
	includeClosed := c.Query("include_closed") == "true"

	positions, total, err := h.db.ListPositions(callerID(c), includeClosed, opts)
	if err != nil {
		respondDBError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"data":       positions,
		"pagination": paginationMeta(opts, total),
	})
}

// Summary handles GET /api/v1/positions/summary and returns the caller's
// portfolio totals.
func (h *PositionHandler) Summary(c *gin.Context) {
	summary, err := h.db.PortfolioSummary(callerID(c))
	if err != nil {
		respondDBError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": summary})
}

// Detail handles GET /api/v1/positions/:instrument_id, always scoped to the
// caller: one holding's position, what it has banked over its whole life, and
// its own money-weighted return.
//
// An instrument the caller has never traded is 404 rather than an empty
// holding, and so is one another user holds — the same rule the ledger follows,
// so an id cannot be probed for existence.
func (h *PositionHandler) Detail(c *gin.Context) {
	detail, err := h.db.HoldingDetail(callerID(c), c.Param("instrument_id"), time.Now())
	if err != nil {
		respondDBError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": detail})
}
