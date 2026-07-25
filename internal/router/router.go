// Package router wires the handlers, middleware and routes into a Gin engine.
package router

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"stockbook/internal/auth"
	"stockbook/internal/db"
	"stockbook/internal/handlers"
	"stockbook/internal/middleware"
	"stockbook/internal/models"
)

// New builds and configures the Gin router. am signs and verifies bearer
// tokens; log receives one structured record per request; provider supplies
// market data and may be nil, in which case the quote endpoints report that
// quote lookup is not configured.
func New(s *db.DB, am *auth.Manager, log *slog.Logger, provider handlers.QuoteProvider) *gin.Engine {
	r := gin.New()
	r.Use(middleware.RequestID(), middleware.Logger(log), gin.Recovery(), cors())

	authH := handlers.NewAuthHandler(s, am)
	user := handlers.NewUserHandler(s)
	instrument := handlers.NewInstrumentHandler(s, provider)
	transaction := handlers.NewTransactionHandler(s)
	position := handlers.NewPositionHandler(s)

	// Health check (also verifies the database connection)
	r.GET("/health", func(c *gin.Context) {
		if err := s.Ping(); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	v1 := r.Group("/api/v1")
	{
		a := v1.Group("/auth")
		{
			a.POST("/register", authH.Register)
			// Rate-limit login per client IP to slow credential brute-forcing.
			a.POST("/login", middleware.RateLimit(10, time.Minute), authH.Login)
			a.PUT("/password", middleware.RequireAuth(am, s), authH.ChangePassword)
		}

		// User management: admin-only.
		u := v1.Group("/users", middleware.RequireRole(am, s, models.RoleAdmin))
		{
			u.GET("", user.List)
			u.POST("", user.Create)
			u.PUT("/:id/role", user.UpdateRole)
			u.PUT("/:id/password", user.ResetPassword)
			u.DELETE("/:id", user.Delete)
		}

		// Instruments are shared master data: any signed-in user may read them
		// (they need the list to enter a trade), but only an admin may change
		// them. Quotes are a separate route from the rest of the record because
		// keeping prices current is routine daily work while editing the master
		// data is not.
		i := v1.Group("/instruments", middleware.RequireAuth(am, s))
		{
			i.GET("", instrument.List)
			i.GET("/:id", instrument.Get)
			// Looking a symbol up before adding it is what keeps the master data
			// free of instruments the provider has never heard of. Admin-only
			// like the create it feeds, and rate-limited because each call
			// reaches the provider.
			i.GET("/search", middleware.RequireRole(am, s, models.RoleAdmin),
				middleware.RateLimit(30, time.Minute), instrument.Search)
			i.POST("", middleware.RequireRole(am, s, models.RoleAdmin), instrument.Create)
			i.PUT("/:id", middleware.RequireRole(am, s, models.RoleAdmin), instrument.Update)
			i.PATCH("/:id/price", middleware.RequireRole(am, s, models.RoleAdmin), instrument.SetPrice)
			// Fetching quotes reaches an external provider, so it is an explicit
			// action rather than a background job: a caller who triggers it gets
			// the per-symbol failures back instead of them landing in a log.
			//
			// Any signed-in user may run it. A holdings page without current
			// prices shows no unrealized profit or loss, so making this an
			// admin's job would leave everyone else unable to fix a stale book,
			// and a market price is shared objective data rather than something
			// the caller owns. The rate limit is what protects the outbound
			// dependency; the handler additionally leaves current quotes alone.
			i.POST("/refresh-quotes", middleware.RateLimit(6, time.Minute), instrument.RefreshQuotes)
			i.DELETE("/:id", middleware.RequireRole(am, s, models.RoleAdmin), instrument.Delete)
		}

		// A ledger is strictly personal: every route below is scoped to the
		// caller in the db query itself, and an admin has no privileged view of
		// someone else's book. Admin power covers the shared master data and
		// accounts, not other people's holdings.
		t := v1.Group("/transactions", middleware.RequireAuth(am, s))
		{
			t.GET("", transaction.List)
			t.POST("", transaction.Create)
			t.GET("/:id", transaction.Get)
			t.PUT("/:id", transaction.Update)
			t.DELETE("/:id", transaction.Delete)
		}

		p := v1.Group("/positions", middleware.RequireAuth(am, s))
		{
			p.GET("", position.List)
			p.GET("/summary", position.Summary)
		}
	}

	return r
}

// cors is a permissive CORS middleware suitable for local development.
func cors() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
