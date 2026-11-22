package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
)

func Recovery(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if value := recover(); value != nil {
				id := GetRequestID(c)
				log.ErrorContext(c.Request.Context(), "http panic",
					"panic", value, "stack", string(debug.Stack()), "request_id", id)
				if c.Writer.Written() {
					c.Abort()
					return
				}
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"error":      "internal server error",
					"request_id": id,
				})
			}
		}()
		c.Next()
	}
}
