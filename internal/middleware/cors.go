package middleware

import (
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

	"projectlens/internal/config"
)

func CORS(cfg config.CORSConfig) gin.HandlerFunc {
	origins := slices.Clone(cfg.AllowedOrigins)
	allowAnyOrigin := slices.Contains(origins, "*")
	methods := strings.Join(cfg.AllowedMethods, ", ")
	headers := strings.Join(cfg.AllowedHeaders, ", ")

	return func(c *gin.Context) {
		if len(origins) > 0 && !allowAnyOrigin {
			c.Writer.Header().Add("Vary", "Origin")
		}
		origin := c.GetHeader("Origin")
		if allowAnyOrigin || (origin != "" && slices.Contains(origins, origin)) {
			if allowAnyOrigin {
				origin = "*"
			}
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Methods", methods)
			c.Header("Access-Control-Allow-Headers", headers)
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
