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

type CachePinger interface {
	Ping(context.Context) error
}

// The cache is optional. PostgreSQL determines the readiness status.
func ReadyHandler(pinger Pinger, cachePinger ...CachePinger) gin.HandlerFunc {
	return func(c *gin.Context) {
		checks := gin.H{"postgres": "ok", "redis": "disabled"}
		status, httpStatus := "ok", http.StatusOK
		ctx := c.Request.Context()
		if pinger != nil && pinger.Ping(ctx) != nil {
			checks["postgres"] = "unavailable"
			status, httpStatus = "unavailable", http.StatusServiceUnavailable
		}
		if len(cachePinger) > 0 && cachePinger[0] != nil {
			checks["redis"] = "ok"
			if cachePinger[0].Ping(ctx) != nil {
				checks["redis"] = "degraded"
			}
		}
		c.JSON(httpStatus, gin.H{"status": status, "checks": checks})
	}
}
