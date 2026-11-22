package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"projectlens/internal/config"
)

func HealthHandler(app config.AppConfig) gin.HandlerFunc {
	startedAt := time.Now()
	return func(c *gin.Context) {
		now := time.Now()
		c.JSON(http.StatusOK, gin.H{
			"status":    "ok",
			"service":   app.Name,
			"env":       app.Env,
			"version":   app.Version,
			"timestamp": now.UTC().Format(time.RFC3339),
			"uptime":    now.Sub(startedAt).String(),
		})
	}
}

type Pinger interface {
	Ping(context.Context) error
}

func ReadyHandler(pinger Pinger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if pinger != nil && pinger.Ping(c.Request.Context()) != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
}
