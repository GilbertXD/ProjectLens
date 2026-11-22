package handler

import (
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
