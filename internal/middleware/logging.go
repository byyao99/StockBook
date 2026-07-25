// Package middleware holds cross-cutting Gin middleware: request-ID
// propagation, structured request logging, and authentication/authorization.
package middleware

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// RequestIDKey is the Gin context key (and structured-log field) under which the
// per-request correlation ID is stored. RequestIDHeader is the HTTP header used
// to read an inbound ID and echo the effective one back to the client.
const (
	RequestIDKey    = "request_id"
	RequestIDHeader = "X-Request-ID"
)

// RequestID ensures every request carries a correlation ID. It reuses a
// caller-supplied X-Request-ID when present, otherwise generates a UUID, stores
// it in the Gin context for downstream handlers, and echoes it in the response.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(RequestIDHeader)
		if id == "" {
			id = uuid.NewString()
		}
		c.Set(RequestIDKey, id)
		c.Header(RequestIDHeader, id)
		c.Next()
	}
}

// RequestIDFromContext returns the correlation ID set by RequestID, or "" if
// the middleware did not run.
func RequestIDFromContext(c *gin.Context) string {
	return c.GetString(RequestIDKey)
}

// Logger emits one structured slog record per request once it completes,
// including method, path, status, latency, client IP and the correlation ID.
// It replaces Gin's default text logger; pair it with gin.Recovery().
func Logger(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		status := c.Writer.Status()
		attrs := []any{
			slog.String(RequestIDKey, RequestIDFromContext(c)),
			slog.String("method", c.Request.Method),
			slog.String("path", c.Request.URL.Path),
			slog.Int("status", status),
			slog.Duration("latency", time.Since(start)),
			slog.String("client_ip", c.ClientIP()),
		}
		if len(c.Errors) > 0 {
			attrs = append(attrs, slog.String("errors", c.Errors.String()))
		}

		msg := "request"
		switch {
		case status >= 500:
			log.Error(msg, attrs...)
		case status >= 400:
			log.Warn(msg, attrs...)
		default:
			log.Info(msg, attrs...)
		}
	}
}
