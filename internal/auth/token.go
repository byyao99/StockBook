package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ErrInvalidToken is returned by Manager.Verify for any malformed, tampered,
// or expired token. The specific reason is deliberately not exposed to callers.
var ErrInvalidToken = errors.New("invalid or expired token")

// Claims is the verified payload carried by a token. TokenVersion is compared
// against the account's current version on each request, so bumping the stored
// version revokes every previously issued token.
type Claims struct {
	Subject      string `json:"sub"` // user ID
	Username     string `json:"username"`
	Role         string `json:"role"`
	TokenVersion int    `json:"token_version"`
	ExpiresAt    int64  `json:"exp"` // Unix seconds
}

// Manager issues and verifies stateless bearer tokens. A token is the
// base64url-encoded JSON claims joined by "." to an HMAC-SHA256 signature over
// those bytes, so it can be validated without a database lookup. The same
// secret must be used to issue and verify; rotating the secret invalidates all
// outstanding tokens.
type Manager struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time // injectable clock for tests
}

// NewManager returns a Manager that signs tokens with secret and grants them
// the given lifetime.
func NewManager(secret []byte, ttl time.Duration) *Manager {
	return &Manager{secret: secret, ttl: ttl, now: time.Now}
}

var enc = base64.RawURLEncoding

// Issue mints a signed token for the given user identity.
func (m *Manager) Issue(subject, username, role string, tokenVersion int) (string, error) {
	claims := Claims{
		Subject:      subject,
		Username:     username,
		Role:         role,
		TokenVersion: tokenVersion,
		ExpiresAt:    m.now().Add(m.ttl).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encoded := enc.EncodeToString(payload)
	return encoded + "." + enc.EncodeToString(m.sign([]byte(encoded))), nil
}

// Verify checks a token's signature and expiry and returns its claims.
// It returns ErrInvalidToken if the token is malformed, tampered, or expired.
func (m *Manager) Verify(token string) (*Claims, error) {
	encoded, sigPart, ok := strings.Cut(token, ".")
	if !ok {
		return nil, ErrInvalidToken
	}
	sig, err := enc.DecodeString(sigPart)
	if err != nil || !hmac.Equal(sig, m.sign([]byte(encoded))) {
		return nil, ErrInvalidToken
	}
	payload, err := enc.DecodeString(encoded)
	if err != nil {
		return nil, ErrInvalidToken
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, ErrInvalidToken
	}
	if m.now().Unix() >= claims.ExpiresAt {
		return nil, ErrInvalidToken
	}
	return &claims, nil
}

// sign computes the HMAC-SHA256 of data under the manager's secret.
func (m *Manager) sign(data []byte) []byte {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write(data)
	return mac.Sum(nil)
}
