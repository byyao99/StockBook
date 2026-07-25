package auth

import (
	"testing"
	"time"
)

func TestPasswordHashAndCheck(t *testing.T) {
	hash, err := HashPassword("s3cret-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "s3cret-password" {
		t.Fatal("password was stored in plaintext")
	}
	if !CheckPassword(hash, "s3cret-password") {
		t.Error("correct password rejected")
	}
	if CheckPassword(hash, "wrong-password") {
		t.Error("wrong password accepted")
	}
}

func TestTokenRoundTrip(t *testing.T) {
	m := NewManager([]byte("test-secret"), time.Hour)
	token, err := m.Issue("user-1", "alice", "admin", 3)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	claims, err := m.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Subject != "user-1" || claims.Username != "alice" || claims.Role != "admin" || claims.TokenVersion != 3 {
		t.Errorf("unexpected claims: %+v", claims)
	}
}

func TestVerifyRejectsTampering(t *testing.T) {
	m := NewManager([]byte("test-secret"), time.Hour)
	token, _ := m.Issue("user-1", "alice", "customer", 0)

	// Flip the last character of the token to break the signature.
	tampered := token[:len(token)-1] + flip(token[len(token)-1])
	if _, err := m.Verify(tampered); err != ErrInvalidToken {
		t.Errorf("tampered token: got %v, want ErrInvalidToken", err)
	}

	// A different secret must not validate the token.
	other := NewManager([]byte("other-secret"), time.Hour)
	if _, err := other.Verify(token); err != ErrInvalidToken {
		t.Errorf("wrong secret: got %v, want ErrInvalidToken", err)
	}

	if _, err := m.Verify("not-a-token"); err != ErrInvalidToken {
		t.Errorf("malformed token: got %v, want ErrInvalidToken", err)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	m := NewManager([]byte("test-secret"), time.Hour)
	// Freeze the clock in the past so the freshly issued token is already expired.
	m.now = func() time.Time { return time.Now().Add(-2 * time.Hour) }
	token, _ := m.Issue("user-1", "alice", "customer", 0)
	m.now = time.Now
	if _, err := m.Verify(token); err != ErrInvalidToken {
		t.Errorf("expired token: got %v, want ErrInvalidToken", err)
	}
}

func flip(b byte) string {
	if b == 'A' {
		return "B"
	}
	return "A"
}
