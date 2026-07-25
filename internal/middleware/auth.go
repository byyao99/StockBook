package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"stockbook/internal/auth"
	"stockbook/internal/models"
)

// Gin context keys under which the authenticated identity is stored.
const (
	ContextUserIDKey   = "user_id"
	ContextUsernameKey = "username"
	ContextRoleKey     = "role"
)

// UserSource looks up accounts so tokens can be checked against live account
// state. *db.DB satisfies it.
type UserSource interface {
	GetUser(id string) (models.User, error)
}

// RequireAuth rejects requests without a valid bearer token. On success it
// stores the caller's identity in the Gin context for downstream handlers.
func RequireAuth(am *auth.Manager, users UserSource) gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, ok := authenticate(c, am, users); ok {
			c.Next()
		}
	}
}

// RequireRole rejects requests whose token is missing/invalid (401) or whose
// role is not in allowed (403).
func RequireRole(am *auth.Manager, users UserSource, allowed ...models.Role) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := authenticate(c, am, users)
		if !ok {
			return
		}
		for _, r := range allowed {
			if user.Role == r {
				c.Next()
				return
			}
		}
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "insufficient permissions"})
	}
}

// authenticate extracts and verifies the bearer token, then checks it against
// the live account: the user must still exist and the token's version must
// match the account's current one, so password changes (which bump the
// version), role changes, and deletions take effect immediately instead of
// when the token expires. On failure it writes a 401, aborts the chain, and
// returns ok=false; on success it records the identity (from the DB record,
// not the token) in the context.
func authenticate(c *gin.Context, am *auth.Manager, users UserSource) (models.User, bool) {
	header := c.GetHeader("Authorization")
	token, found := strings.CutPrefix(header, "Bearer ")
	if !found || token == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing or malformed authorization header"})
		return models.User{}, false
	}
	claims, err := am.Verify(token)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return models.User{}, false
	}
	user, err := users.GetUser(claims.Subject)
	if err != nil || user.TokenVersion != claims.TokenVersion {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return models.User{}, false
	}
	c.Set(ContextUserIDKey, user.ID)
	c.Set(ContextUsernameKey, user.Username)
	c.Set(ContextRoleKey, string(user.Role))
	return user, true
}
