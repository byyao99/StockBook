package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"stockbook/internal/db"
	"stockbook/internal/models"
)

// PlanHandler manages savings plans and the instalments they have come due for.
//
// A plan is configuration, not ledger: it says what is supposed to happen and
// never that it did. Everything here is scoped to the caller, like fee terms.
type PlanHandler struct {
	db *db.DB
}

// NewPlanHandler creates a PlanHandler.
func NewPlanHandler(s *db.DB) *PlanHandler {
	return &PlanHandler{db: s}
}

// createPlanRequest is a standing instruction to buy.
//
// Amount is the cash debited each time, because that is what a savings plan
// actually fixes — the share count falls out of the morning's price and is not
// knowable in advance.
type createPlanRequest struct {
	InstrumentID string `json:"instrument_id" binding:"required"`
	DaysOfMonth  string `json:"days_of_month" binding:"required"`
	Amount       int64  `json:"amount" binding:"required,gt=0"`
	StartedOn    string `json:"started_on" binding:"required"`
}

// List handles GET /api/v1/plans.
func (h *PlanHandler) List(c *gin.Context) {
	plans, err := h.db.ListPlans(callerID(c))
	if err != nil {
		respondDBError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": plans})
}

// Create handles POST /api/v1/plans.
func (h *PlanHandler) Create(c *gin.Context) {
	var req createPlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	days, err := db.ParsePlanDays(req.DaysOfMonth)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if _, err := time.Parse(time.DateOnly, req.StartedOn); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "started_on must be YYYY-MM-DD"})
		return
	}
	if _, err := h.db.GetInstrument(req.InstrumentID); err != nil {
		respondDBError(c, err)
		return
	}

	plan, err := h.db.CreatePlan(models.RecurringPlan{
		ID:           uuid.NewString(),
		UserID:       callerID(c),
		InstrumentID: req.InstrumentID,
		// Stored canonically, however it was typed, so two plans on the same
		// schedule cannot read as different ones.
		DaysOfMonth: db.FormatPlanDays(days),
		Amount:      req.Amount,
		StartedOn:   req.StartedOn,
	})
	if err != nil {
		respondDBError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": plan})
}

// End handles PUT /api/v1/plans/:id/end: stop a plan without erasing it.
//
// A finished plan is kept rather than deleted, because the purchases it
// prompted are still in the ledger and it is the only record of why they are
// spaced as they are.
func (h *PlanHandler) End(c *gin.Context) {
	on := c.Query("on")
	if on == "" {
		on = time.Now().UTC().Format(time.DateOnly)
	}
	if _, err := time.Parse(time.DateOnly, on); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "on must be YYYY-MM-DD"})
		return
	}
	if err := h.db.EndPlan(callerID(c), c.Param("id"), on); err != nil {
		respondDBError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Delete handles DELETE /api/v1/plans/:id, for one entered by mistake. Ending a
// plan is what a finished one wants; this erases it.
func (h *PlanHandler) Delete(c *gin.Context) {
	if err := h.db.DeletePlan(callerID(c), c.Param("id")); err != nil {
		respondDBError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Pending handles GET /api/v1/plans/pending: instalments that came due with
// nothing recorded against them.
//
// A prompt, never a posting. The estimate comes from the stored close on the
// due date and is the best guess available, not a claim — the fill happened at
// whatever the market did that morning. Recording it stays the caller's act,
// which is what keeps the ledger a record of what they say happened.
func (h *PlanHandler) Pending(c *gin.Context) {
	pending, err := h.db.PendingPurchases(callerID(c), time.Now().UTC())
	if err != nil {
		respondDBError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": pending})
}
