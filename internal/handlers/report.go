package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"stockbook/internal/db"
)

// ReportHandler serves derived views over a user's own ledger.
type ReportHandler struct {
	db *db.DB
}

// NewReportHandler creates a ReportHandler.
func NewReportHandler(s *db.DB) *ReportHandler {
	return &ReportHandler{db: s}
}

// Realized handles GET /api/v1/reports/realized?from=&to=, always scoped to the
// caller. Both bounds are optional; omitting them reports the whole ledger.
//
// The response is a slice with one entry per currency, never a single total —
// the same rule the portfolio summary follows, for the same reason: there is no
// FX rate in this system, so a TWD gain and a USD gain cannot be added.
func (h *ReportHandler) Realized(c *gin.Context) {
	from, to, err := parseDateRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	report, err := h.db.RealizedReport(callerID(c), from, to)
	if err != nil {
		respondDBError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": report})
}

// Returns handles GET /api/v1/reports/returns, always scoped to the caller: the
// annualized money-weighted rate of return on their book, one entry per currency.
//
// It takes no date range, unlike Realized. A return measured over a window needs
// the market value of the portfolio on the day the window opened, and this
// system stores only each instrument's current quote — the book's value on any
// past date cannot be recovered. The period is therefore always since the first
// entry in the ledger, and accepting bounds would promise otherwise.
func (h *ReportHandler) Returns(c *gin.Context) {
	report, err := h.db.ReturnsReport(callerID(c), time.Now())
	if err != nil {
		respondDBError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": report})
}
