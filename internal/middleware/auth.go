package middleware

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"projectlens/internal/config"
)

const authStateKey = "auth"

func BearerAuth(cfg config.AuthConfig) gin.HandlerFunc {
	digests := make([][sha256.Size]byte, len(cfg.BearerTokens))
	for i, token := range cfg.BearerTokens {
		digests[i] = sha256.Sum256([]byte(token))
	}
	return func(c *gin.Context) {
		if !cfg.Enabled {
			c.Set(authStateKey, "disabled")
			c.Next()
			return
		}
		values := c.Request.Header.Values("Authorization")
		if len(values) == 1 {
			fields := strings.Fields(values[0])
			if len(fields) == 2 && strings.HasPrefix(values[0], fields[0]) &&
				strings.EqualFold(fields[0], "Bearer") && !strings.ContainsRune(fields[1], 0) {
				presented := sha256.Sum256([]byte(fields[1]))
				match := 0
				for _, digest := range digests {
					match |= subtle.ConstantTimeCompare(presented[:], digest[:])
				}
				if match == 1 {
					c.Set(authStateKey, "authenticated")
					c.Next()
					return
				}
			}
		}
		c.Set(authStateKey, "rejected")
		c.Header("WWW-Authenticate", `Bearer realm="ProjectLens"`)
		abortError(c, http.StatusUnauthorized, "unauthorized", "unauthorized")
	}
}
