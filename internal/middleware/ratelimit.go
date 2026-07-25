package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// RateLimit allows at most max requests per fixed window per client IP and
// responds 429 beyond that. State is in-memory and per-process, which is
// sufficient for a single-instance deployment; a multi-instance deployment
// would need a shared limiter instead.
func RateLimit(max int, window time.Duration) gin.HandlerFunc {
	type bucket struct {
		count       int
		windowStart time.Time
	}
	var (
		mu      sync.Mutex
		buckets = make(map[string]*bucket)
	)
	return func(c *gin.Context) {
		now := time.Now()
		key := c.ClientIP()

		mu.Lock()
		// Sweep expired buckets once the map grows large, so one-off clients
		// cannot make it grow without bound.
		if len(buckets) > 4096 {
			for k, b := range buckets {
				if now.Sub(b.windowStart) >= window {
					delete(buckets, k)
				}
			}
		}
		b := buckets[key]
		if b == nil || now.Sub(b.windowStart) >= window {
			b = &bucket{windowStart: now}
			buckets[key] = b
		}
		b.count++
		exceeded := b.count > max
		mu.Unlock()

		if exceeded {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "too many attempts, please try again later"})
			return
		}
		c.Next()
	}
}
