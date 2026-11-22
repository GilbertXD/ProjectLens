package middleware

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/gin-gonic/gin"
)

const (
	RequestIDHeader = "X-Request-ID"
	RequestIDKey    = "request_id"
)

func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(RequestIDHeader)
		if id == "" {
			var random [16]byte
			// Since Go 1.24, Read always fills the buffer or aborts the process.
			rand.Read(random[:])
			id = hex.EncodeToString(random[:])
		}
		c.Set(RequestIDKey, id)
		c.Header(RequestIDHeader, id)
		c.Next()
	}
}

func GetRequestID(c *gin.Context) string {
	return c.GetString(RequestIDKey)
}
