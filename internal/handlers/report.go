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

// Hindsight handles GET /api/v1/reports/hindsight?from=&to=, always scoped to
// the caller: what the sales in that period would be worth had the shares never
// been sold. Both bounds are optional and behave exactly as Realized's do.
//
// Unlike Returns it takes a period happily, because the comparison is always
// against *today's* quote — no historical price is needed to ask it about a
// past year.
func (h *ReportHandler) Hindsight(c *gin.Context) {
	from, to, err := parseDateRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	report, err := h.db.HindsightReport(callerID(c), from, to)
	if err != nil {
		respondDBError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": report})
}

// Curve handles GET /api/v1/reports/curve?from=&to=, always scoped to the
// caller: the daily history of their book, one entry per currency.
//
// Bounds are optional YYYY-MM-DD dates. They select sessions rather than trades,
// so unlike the other reports the period does not change what is counted — the
// whole ledger is always folded, and the window only decides which days are
// reported. Narrowing it to last month still shows holdings bought years ago.
func (h *ReportHandler) Curve(c *gin.Context) {
	from, to := c.Query("from"), c.Query("to")
	for _, bound := range []string{from, to} {
		if bound == "" {
			continue
		}
		if _, err := time.Parse(time.DateOnly, bound); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "dates must be YYYY-MM-DD"})
			return
		}
	}

	curves, err := h.db.EquityCurve(callerID(c), from, to)
	if err != nil {
		respondDBError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": curves})
}

// Returns handles GET /api/v1/reports/returns, always scoped to the caller: the
// annualized money-weighted rate of return on their book, one entry per currency.
//
// With no bounds it measures since the first entry in the ledger, closing on the
// live quote for whatever is still held. With either bound it measures that
// window instead, which is a different question and not a filtered version of
// the same one — a period rate has to open with the position the period was
// entered holding.
//
// The window used to be refused outright, on the grounds that the book's value
// on a past date was not recoverable. That was true until daily closes were
// stored; it is not any more, and a windowed report is valued from them by the
// same rules the equity curve follows.
func (h *ReportHandler) Returns(c *gin.Context) {
	from, to := c.Query("from"), c.Query("to")
	for _, bound := range []string{from, to} {
		if bound == "" {
			continue
		}
		if _, err := time.Parse(time.DateOnly, bound); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "dates must be YYYY-MM-DD"})
			return
		}
	}

	if from == "" && to == "" {
		report, err := h.db.ReturnsReport(callerID(c), time.Now())
		if err != nil {
			respondDBError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": report})
		return
	}

	report, err := h.db.ReturnsBetween(callerID(c), from, to)
	if err != nil {
		respondDBError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": report})
}

// Dividends handles GET /api/v1/reports/dividends, scoped to the caller: the
// distributions their book was entitled to and has no ledger entry for.
//
// It is a prompt and never a posting. The provider knows what a security paid
// and the ledger knows what the user banked; those are different facts with
// different owners, and writing the first into the second would invent entries.
// What this does is close the gap that actually loses money — a payout arrives
// weeks after anyone was thinking about the stock, and simply never gets written
// down. Nothing else in this system can remind anybody of that.
func (h *ReportHandler) Dividends(c *gin.Context) {
	pending, err := h.db.PendingDividends(callerID(c))
	if err != nil {
		respondDBError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": pending})
}

// splitAckRequest names one split to acknowledge or to un-acknowledge.
//
// The pair travels in a body rather than in the path because a date in a URL
// segment is awkward to read back and the two together are one identity, not a
// resource with a name of its own.
type splitAckRequest struct {
	InstrumentID string `json:"instrument_id" binding:"required"`
	Date         string `json:"date" binding:"required"`
}

// Splits handles GET /api/v1/reports/splits?include_acknowledged=, scoped to
// the caller: the splits their book held shares across.
//
// This is a warning rather than a prompt, which is the difference between it
// and the dividend report beside it. A pending dividend asks for an entry that
// will settle it; a split has no entry that would, because this system does not
// model splits. What it reports is that the ledger and the stored prices have
// stopped agreeing about what a share is, and that every figure spanning the
// date is wrong until its owner does something about it by hand.
func (h *ReportHandler) Splits(c *gin.Context) {
	includeAcknowledged := c.Query("include_acknowledged") == "true"
	splits, err := h.db.UnadjustedSplits(callerID(c), includeAcknowledged)
	if err != nil {
		respondDBError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": splits})
}

// AcknowledgeSplit handles POST /api/v1/reports/splits/ack: the caller has seen
// this one and does not want to be told again. A split that names no stored
// event is 404 rather than a silently stored row that dismisses nothing.
func (h *ReportHandler) AcknowledgeSplit(c *gin.Context) {
	var req splitAckRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.AcknowledgeSplit(callerID(c), req.InstrumentID, req.Date); err != nil {
		respondDBError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ForgetSplitAcknowledgement handles DELETE /api/v1/reports/splits/ack: bring
// the warning back. Dismissing one by accident must not be permanent, since
// nothing else in the system will ever mention the split again.
func (h *ReportHandler) ForgetSplitAcknowledgement(c *gin.Context) {
	var req splitAckRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.ForgetSplitAcknowledgement(callerID(c), req.InstrumentID, req.Date); err != nil {
		respondDBError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
