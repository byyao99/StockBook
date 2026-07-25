package handlers_test

import (
	"net/http"
	"testing"

	"stockbook/internal/models"
)

func TestRegisterAlwaysCreatesAPlainUser(t *testing.T) {
	e := setup(t)

	// Self-registration must never honor a requested role, or anyone could
	// provision themselves an admin account.
	rec := e.do(t, http.MethodPost, "/api/v1/auth/register", map[string]any{
		"username": "climber", "password": "password123", "role": "admin",
	}, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	var session struct {
		Token string      `json:"token"`
		User  models.User `json:"user"`
	}
	decodeData(t, rec, &session)
	if session.User.Role != models.RoleUser {
		t.Errorf("role = %q, want user", session.User.Role)
	}
	// And the token it hands back really is unprivileged.
	if rec := e.do(t, http.MethodGet, "/api/v1/users", nil, session.Token); rec.Code != http.StatusForbidden {
		t.Errorf("new account reaching /users: got %d, want 403", rec.Code)
	}
}

func TestPasswordChangeRevokesOutstandingTokens(t *testing.T) {
	e := setup(t)
	oldToken := e.token(t, "alice", models.RoleUser) // seeded with password123

	if rec := e.do(t, http.MethodGet, "/api/v1/positions", nil, oldToken); rec.Code != http.StatusOK {
		t.Fatalf("before change: got %d, want 200", rec.Code)
	}

	body := map[string]any{"old_password": "password123", "new_password": "newpass123"}
	rec := e.do(t, http.MethodPut, "/api/v1/auth/password", body, oldToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("change password: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	var session struct {
		Token string `json:"token"`
	}
	decodeData(t, rec, &session)

	// The pre-change token is revoked; the returned one works.
	if rec := e.do(t, http.MethodGet, "/api/v1/positions", nil, oldToken); rec.Code != http.StatusUnauthorized {
		t.Errorf("old token after change: got %d, want 401", rec.Code)
	}
	if rec := e.do(t, http.MethodGet, "/api/v1/positions", nil, session.Token); rec.Code != http.StatusOK {
		t.Errorf("fresh token after change: got %d, want 200", rec.Code)
	}
}

func TestDeletedUserTokenStopsWorking(t *testing.T) {
	e := setup(t)
	admin := e.token(t, "boss", models.RoleAdmin)
	victim := e.token(t, "alice", models.RoleUser)
	target, err := e.s.GetUserByUsername("alice")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}

	if rec := e.do(t, http.MethodDelete, "/api/v1/users/"+target.ID, nil, admin); rec.Code != http.StatusNoContent {
		t.Fatalf("delete user: got %d", rec.Code)
	}
	// Deletion takes effect immediately, not at token expiry.
	if rec := e.do(t, http.MethodGet, "/api/v1/positions", nil, victim); rec.Code != http.StatusUnauthorized {
		t.Errorf("deleted user's token: got %d, want 401", rec.Code)
	}
}

// A role change must take effect on the next request, not at token expiry —
// which is why authenticate reloads the account rather than trusting the claims.
func TestRoleChangeTakesEffectImmediately(t *testing.T) {
	e := setup(t)
	admin := e.token(t, "boss", models.RoleAdmin)
	promoted := e.token(t, "alice", models.RoleUser)
	target, err := e.s.GetUserByUsername("alice")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}

	if rec := e.do(t, http.MethodGet, "/api/v1/users", nil, promoted); rec.Code != http.StatusForbidden {
		t.Fatalf("before promotion: got %d, want 403", rec.Code)
	}
	rec := e.do(t, http.MethodPut, "/api/v1/users/"+target.ID+"/role", map[string]any{"role": "admin"}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("promote: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	// Same token, new privileges — the identity comes from the database record.
	if rec := e.do(t, http.MethodGet, "/api/v1/users", nil, promoted); rec.Code != http.StatusOK {
		t.Errorf("after promotion: got %d, want 200", rec.Code)
	}
}

func TestAdminCannotLockThemselvesOut(t *testing.T) {
	e := setup(t)
	admin := e.token(t, "boss", models.RoleAdmin)
	self, err := e.s.GetUserByUsername("boss")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}

	rec := e.do(t, http.MethodPut, "/api/v1/users/"+self.ID+"/role", map[string]any{"role": "user"}, admin)
	if rec.Code != http.StatusForbidden {
		t.Errorf("demoting self: got %d, want 403", rec.Code)
	}
	if rec := e.do(t, http.MethodDelete, "/api/v1/users/"+self.ID, nil, admin); rec.Code != http.StatusForbidden {
		t.Errorf("deleting self: got %d, want 403", rec.Code)
	}
}

func TestTamperedTokenIsRejected(t *testing.T) {
	e := setup(t)
	valid := e.token(t, "alice", models.RoleUser)

	for name, token := range map[string]string{
		"flipped payload byte": "x" + valid[1:],
		"no signature":         valid[:len(valid)-10],
		"not a token":          "garbage",
		"empty":                "",
	} {
		if rec := e.do(t, http.MethodGet, "/api/v1/positions", nil, token); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: got %d, want 401", name, rec.Code)
		}
	}
}

func TestLoginIsRateLimited(t *testing.T) {
	e := setup(t)
	body := map[string]any{"username": "nobody", "password": "wrongpass1"}

	// The limiter allows 10 attempts per IP per minute; the 11th must be 429.
	for i := 0; i < 10; i++ {
		if rec := e.do(t, http.MethodPost, "/api/v1/auth/login", body, ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, want 401", i+1, rec.Code)
		}
	}
	if rec := e.do(t, http.MethodPost, "/api/v1/auth/login", body, ""); rec.Code != http.StatusTooManyRequests {
		t.Errorf("11th attempt: got %d, want 429", rec.Code)
	}
}
