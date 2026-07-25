// Package handlers holds the Gin HTTP handlers. They validate input, enforce
// per-role rules, and delegate all persistence to the db layer.
package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"stockbook/internal/db"
	"stockbook/internal/middleware"
	"stockbook/internal/models"
)

const (
	defaultLimit = 20
	maxLimit     = 100

	// minUsernameLen mirrors the binding tag, re-checked after normalization
	// trims surrounding whitespace.
	minUsernameLen = 3
	// maxPasswordLen is bcrypt's working limit: it only hashes the first 72
	// bytes, so anything longer gives a false sense of strength.
	maxPasswordLen = 72
)

// normalizeUsername trims surrounding whitespace and lowercases the username so
// that accounts are case- and whitespace-insensitive (Alice == alice == "alice ").
func normalizeUsername(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// validatePassword enforces the account password rules shared by registration
// and admin provisioning: at most 72 bytes (bcrypt's limit) and at least two of
// {lowercase letter, uppercase letter, digit}. It returns nil when the password
// is acceptable.
func validatePassword(pw string) error {
	if len(pw) > maxPasswordLen {
		return fmt.Errorf("password must be at most %d bytes", maxPasswordLen)
	}
	if passwordClassCount(pw) < 2 {
		return errors.New("password must contain at least two of: lowercase letter, uppercase letter, digit")
	}
	return nil
}

// passwordClassCount reports how many of these character classes appear in pw:
// lowercase letters, uppercase letters, and digits.
func passwordClassCount(pw string) int {
	var hasLower, hasUpper, hasDigit bool
	for _, r := range pw {
		switch {
		case r >= 'a' && r <= 'z':
			hasLower = true
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= '0' && r <= '9':
			hasDigit = true
		}
	}
	count := 0
	for _, present := range []bool{hasLower, hasUpper, hasDigit} {
		if present {
			count++
		}
	}
	return count
}

// respondDBError maps a db-layer error to an appropriate HTTP response:
// ErrNotFound → 404; ErrInsufficientShares, ErrConflict, ErrSymbolTaken and
// ErrInstrumentInUse → 409; anything else → 500. The underlying error is logged
// on the 500 path but never returned to clients.
func respondDBError(c *gin.Context, err error) {
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
		return
	}
	// These messages are built by our own code from ledger data ("insufficient
	// shares: sell 100 2330 on 2026-07-20"), so they are safe to show, and the
	// offending entry is the only actionable detail we have.
	if errors.Is(err, models.ErrInsufficientShares) {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	if errors.Is(err, db.ErrSymbolTaken) {
		c.JSON(http.StatusConflict, gin.H{"error": "symbol already taken"})
		return
	}
	if errors.Is(err, db.ErrInstrumentInUse) {
		c.JSON(http.StatusConflict, gin.H{"error": "this instrument has transactions and cannot be deleted"})
		return
	}
	if errors.Is(err, db.ErrCurrencyLocked) {
		c.JSON(http.StatusConflict, gin.H{
			"error": "this instrument already has trades, so its currency cannot be changed; " +
				"every price recorded against it is denominated in the current one",
		})
		return
	}
	if errors.Is(err, db.ErrConflict) {
		c.JSON(http.StatusConflict, gin.H{"error": "the position was changed by someone else; reload and try again"})
		return
	}
	slog.Error("db error",
		slog.String(middleware.RequestIDKey, middleware.RequestIDFromContext(c)),
		slog.String("method", c.Request.Method),
		slog.String("path", c.Request.URL.Path),
		slog.Any("error", err),
	)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
}

// parseListOptions reads ?limit, ?offset, ?sort and ?order into a
// db.ListOptions. limit defaults to 20 and is capped at 100; offset
// defaults to 0. sort/order are passed through and validated in the db
// against a per-resource column allowlist.
func parseListOptions(c *gin.Context) db.ListOptions {
	opts := db.ListOptions{Limit: defaultLimit, Sort: c.Query("sort"), Order: c.Query("order")}
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 {
		opts.Limit = v
	}
	if opts.Limit > maxLimit {
		opts.Limit = maxLimit
	}
	if v, err := strconv.Atoi(c.Query("offset")); err == nil && v > 0 {
		opts.Offset = v
	}
	return opts
}

// paginationMeta builds the pagination block returned alongside list data.
func paginationMeta(opts db.ListOptions, total int64) gin.H {
	return gin.H{"limit": opts.Limit, "offset": opts.Offset, "total": total}
}

// callerID returns the authenticated user's ID from the Gin context. Identity
// always comes from the context (set by the auth middleware from the database
// record), never from the request body.
func callerID(c *gin.Context) string {
	return c.GetString(middleware.ContextUserIDKey)
}
