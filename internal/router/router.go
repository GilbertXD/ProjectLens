package router

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"projectlens/internal/config"
	"projectlens/internal/handler"
	"projectlens/internal/middleware"
)

type Dependencies struct {
	Jobs         *handler.JobHandler
	Projects     *handler.ProjectHandler
	Repositories *handler.RepositoryHandler
	Snapshots    *handler.SnapshotHandler
	Pinger       handler.Pinger
}

func New(cfg config.Config, log *slog.Logger, optional ...Dependencies) *gin.Engine {
	var deps Dependencies
	if len(optional) != 0 {
		deps = optional[0]
	}
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
	engine.GET("/readyz", handler.ReadyHandler(deps.Pinger))

	api := engine.Group("/api/v1")
	if deps.Projects != nil {
		deps.Projects.Register(api)
	}
	if deps.Repositories != nil {
		deps.Repositories.Register(api)
	}
	if deps.Snapshots != nil {
		deps.Snapshots.Register(api)
	}
	if deps.Jobs != nil {
		deps.Jobs.Register(api)
	}
	api.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "pong"})
	})
	engine.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	})
	return engine
}
