package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"stockbook/internal/db"
	"stockbook/internal/models"
)

// SettingsHandler serves a user's own preferences. Everything here is scoped to
// the caller: settings belong to the account that set them, and an admin has no
// more business reading someone's brokerage rates than reading their ledger.
type SettingsHandler struct {
	db *db.DB
}

// NewSettingsHandler creates a SettingsHandler.
func NewSettingsHandler(s *db.DB) *SettingsHandler {
	return &SettingsHandler{db: s}
}

// feeProfileRequest is one profile in a settings save.
//
// The three numbers are required rather than optional so a partially filled
// form cannot silently zero a rate: an omitted RatePpm binding to 0 would read
// as "this broker is free", which is a claim, not a blank.
type feeProfileRequest struct {
	Key        models.FeeProfileKey `json:"key" binding:"required"`
	RatePpm    *int64               `json:"rate_ppm" binding:"required"`
	MinFee     *int64               `json:"min_fee" binding:"required"`
	SellTaxPpm *int64               `json:"sell_tax_ppm" binding:"required"`
	// Optional: an omitted discount means full price. Unlike the three above it
	// binds to nil rather than being required, so a client that predates the
	// field keeps working and simply pays list.
	DiscountBps *int64 `json:"discount_bps"`
}

// saveFeeProfilesRequest is the payload for PUT /settings/fees. A caller may
// send any subset; keys left out keep whatever they had.
type saveFeeProfilesRequest struct {
	Profiles []feeProfileRequest `json:"profiles" binding:"required,min=1,dive"`
}

// FeeProfiles handles GET /api/v1/settings/fees.
//
// It always returns the full set in models.FeeProfileKeys order, with whatever
// the user has saved laid over the defaults. Returning only the saved rows
// would make a first visit to the settings page render six blank fields, and a
// blank fee rate is the problem this feature exists to remove.
func (h *SettingsHandler) FeeProfiles(c *gin.Context) {
	saved, err := h.db.FeeProfiles(callerID(c))
	if err != nil {
		respondDBError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": mergeFeeProfiles(saved)})
}

// mergeFeeProfiles lays a user's saved profiles over the defaults, in the
// canonical key order.
//
// The merge is by key presence, not by value, which is what keeps a deliberate
// zero distinct from an unset rate. A user whose broker charges no commission
// saves 0 and gets 0 back; a user who has never opened the page gets the
// default. An unknown key already in the database — a key retired by a later
// version — is dropped rather than returned, so the response shape is always
// exactly the current set.
func mergeFeeProfiles(saved []models.FeeProfile) []models.FeeProfile {
	bySaved := make(map[models.FeeProfileKey]models.FeeProfile, len(saved))
	for _, p := range saved {
		bySaved[p.Key] = p
	}
	merged := make([]models.FeeProfile, 0, len(models.FeeProfileKeys))
	for _, def := range models.DefaultFeeProfiles() {
		if p, ok := bySaved[def.Key]; ok {
			merged = append(merged, p)
			continue
		}
		merged = append(merged, def)
	}
	return merged
}

// SaveFeeProfiles handles PUT /api/v1/settings/fees.
func (h *SettingsHandler) SaveFeeProfiles(c *gin.Context) {
	var req saveFeeProfilesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	profiles := make([]models.FeeProfile, 0, len(req.Profiles))
	seen := make(map[models.FeeProfileKey]bool, len(req.Profiles))
	for _, p := range req.Profiles {
		if !p.Key.Valid() {
			c.JSON(http.StatusBadRequest, gin.H{"error": invalidFeeProfileMessage})
			return
		}
		// A payload naming the same profile twice has no single answer, and
		// letting the upsert pick the last one silently would mean the settings
		// page could save something other than what it showed.
		if seen[p.Key] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "duplicate fee profile: " + string(p.Key)})
			return
		}
		seen[p.Key] = true

		discount := int64(0)
		if p.DiscountBps != nil {
			discount = *p.DiscountBps
		}
		if err := validateFeeProfile(*p.RatePpm, *p.MinFee, *p.SellTaxPpm, discount); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		profiles = append(profiles, models.FeeProfile{
			Key:         p.Key,
			RatePpm:     *p.RatePpm,
			MinFee:      *p.MinFee,
			SellTaxPpm:  *p.SellTaxPpm,
			DiscountBps: discount,
		})
	}

	if err := h.db.SaveFeeProfiles(callerID(c), profiles); err != nil {
		respondDBError(c, err)
		return
	}
	saved, err := h.db.FeeProfiles(callerID(c))
	if err != nil {
		respondDBError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": mergeFeeProfiles(saved)})
}

// fullPriceBps is a discount that takes nothing off — the upper bound on the
// field, and what a zero is read as everywhere downstream.
const fullPriceBps int64 = 10000

// invalidFeeProfileMessage names every profile a caller may set, so one who got
// it wrong is told the alternatives rather than just that this one was refused.
var invalidFeeProfileMessage = "key must be one of: " + joinFeeProfileKeys(models.FeeProfileKeys)

func joinFeeProfileKeys(keys []models.FeeProfileKey) string {
	names := make([]string, 0, len(keys))
	for _, k := range keys {
		names = append(names, string(k))
	}
	return strings.Join(names, ", ")
}

// validateFeeProfile checks one profile's three numbers.
//
// The upper bound on the two rates matters more than the lower one. A negative
// fee is obviously wrong and would be noticed immediately; a rate off by a
// factor of ten is not visible anywhere downstream — it just quietly inflates
// the cost of every trade recorded afterwards. No brokerage charges anything
// near 10%, so the ceiling refuses a typo without ever refusing a real rate.
func validateFeeProfile(ratePpm, minFee, sellTaxPpm, discountBps int64) error {
	if ratePpm < 0 || minFee < 0 || sellTaxPpm < 0 {
		return &validationError{"rate_ppm, min_fee and sell_tax_ppm must not be negative"}
	}
	// Above full price is not a discount, and below zero would pay the user to
	// trade. Zero is allowed and means no discount at all.
	if discountBps < 0 || discountBps > fullPriceBps {
		return &validationError{fmt.Sprintf(
			"discount_bps must be between 0 and %d", fullPriceBps)}
	}
	if ratePpm > models.MaxFeeRatePpm || sellTaxPpm > models.MaxFeeRatePpm {
		return &validationError{fmt.Sprintf(
			"rate_ppm and sell_tax_ppm must be at most %d (%.0f%%)",
			models.MaxFeeRatePpm, float64(models.MaxFeeRatePpm)/10000)}
	}
	return nil
}
