package main

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"stockbook/internal/auth"
	"stockbook/internal/db"
	"stockbook/internal/models"
	"stockbook/internal/router"

	"github.com/google/uuid"
)

// tokenTTL is how long an issued bearer token remains valid.
const tokenTTL = 24 * time.Hour

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	s, err := db.Open(dbPath())
	if err != nil {
		logger.Error("failed to open database", slog.Any("error", err))
		os.Exit(1)
	}
	defer func() {
		if err := s.Close(); err != nil {
			logger.Error("error closing database", slog.Any("error", err))
		}
	}()

	authManager := auth.NewManager(authSecret(logger), tokenTTL)
	seedAdmin(s, logger)

	srv := &http.Server{
		Addr:    ":" + port(),
		Handler: router.New(s, authManager, logger),
	}

	// Start the server in the background so main can wait for a shutdown signal.
	go func() {
		logger.Info("StockBook API listening", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", slog.Any("error", err))
			os.Exit(1)
		}
	}()

	// Block until an interrupt or SIGTERM arrives.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	stop()

	logger.Info("shutting down, draining in-flight requests...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", slog.Any("error", err))
		os.Exit(1)
	}
	logger.Info("server stopped")
}

// port reads the listen port from the PORT env var, defaulting to 8080.
func port() string {
	if p := os.Getenv("PORT"); p != "" {
		return p
	}
	return "8080"
}

// dbPath reads the SQLite file path from DB_PATH, defaulting to stockbook.db.
func dbPath() string {
	if p := os.Getenv("DB_PATH"); p != "" {
		return p
	}
	return "stockbook.db"
}

// authSecret returns the HMAC signing key for bearer tokens. It is read from
// AUTH_SECRET; if unset, a random ephemeral key is generated so the server is
// still secure, at the cost of invalidating all tokens on restart.
func authSecret(logger *slog.Logger) []byte {
	if v := os.Getenv("AUTH_SECRET"); v != "" {
		return []byte(v)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		logger.Error("failed to generate auth secret", slog.Any("error", err))
		os.Exit(1)
	}
	logger.Warn("AUTH_SECRET not set; using a random key — tokens will not survive a restart")
	return secret
}

// seedAdmin creates an initial admin account from ADMIN_USERNAME/ADMIN_PASSWORD
// when no users exist yet. It is a no-op if either var is unset or users already
// exist, so it is safe to run on every startup.
func seedAdmin(s *db.DB, logger *slog.Logger) {
	username, password := os.Getenv("ADMIN_USERNAME"), os.Getenv("ADMIN_PASSWORD")
	if username == "" || password == "" {
		return
	}
	count, err := s.CountUsers()
	if err != nil {
		logger.Error("failed to count users for admin seed", slog.Any("error", err))
		return
	}
	if count > 0 {
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		logger.Error("failed to hash admin password", slog.Any("error", err))
		return
	}
	if _, err := s.CreateUser(models.User{
		ID:           uuid.NewString(),
		Username:     username,
		PasswordHash: hash,
		Role:         models.RoleAdmin,
	}); err != nil {
		logger.Error("failed to seed admin user", slog.Any("error", err))
		return
	}
	logger.Info("seeded initial admin user", slog.String("username", username))
}
