package router

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"projectlens/internal/config"
	"projectlens/internal/handler"
	"projectlens/internal/middleware"
)

func New(cfg config.Config, log *slog.Logger) *gin.Engine {
	switch cfg.App.Env {
	case "production":
		gin.SetMode(gin.ReleaseMode)
	case "test":
		gin.SetMode(gin.TestMode)
	default:
		gin.SetMode(gin.DebugMode)
	}

	engine := gin.New()
	_ = engine.SetTrustedProxies(nil)
	engine.RedirectTrailingSlash = false
	engine.Use(middleware.RequestID(), middleware.Logger(log), middleware.Recovery(log), middleware.CORS(cfg.CORS))
	engine.GET("/healthz", handler.HealthHandler(cfg.App))

	api := engine.Group("/api/v1")
	api.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "pong"})
	})
	engine.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	})
	return engine
}
