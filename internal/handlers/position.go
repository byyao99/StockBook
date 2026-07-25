package handlers

import (
	"net/http"

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
