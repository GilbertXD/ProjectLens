package middleware

import (
	"net/http"
	"slices"

	"github.com/gin-gonic/gin"
)

func abortError(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": message, "code": code, "request_id": GetRequestID(c)})
}

func MaxBodyBytes(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if limit > 0 {
			if c.Request.ContentLength > limit {
				abortError(c, http.StatusRequestEntityTooLarge, "payload_too_large", "payload too large")
				return
			}
			if c.Request.Body != nil {
				c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
			}
		}
		c.Next()
	}
}

func MaxInFlight(limit int, exemptPaths ...string) gin.HandlerFunc {
	slots := make(chan struct{}, max(limit, 0))
	exemptPaths = slices.Clone(exemptPaths)
	return func(c *gin.Context) {
		if limit > 0 && !slices.Contains(exemptPaths, c.Request.URL.Path) {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			default:
				c.Header("Retry-After", "1")
				abortError(c, http.StatusServiceUnavailable, "service_unavailable", "server busy")
				return
			}
		}
		c.Next()
	}
}
