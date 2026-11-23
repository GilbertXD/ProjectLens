package router

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"projectlens/internal/config"
	"projectlens/internal/handler"
	"projectlens/internal/middleware"
)

type Dependencies struct {
	Documents    *handler.DocumentHandler
	Jobs         *handler.JobHandler
	Projects     *handler.ProjectHandler
	Repositories *handler.RepositoryHandler
	Snapshots    *handler.SnapshotHandler
	Search       *handler.SearchHandler
	Pinger       handler.Pinger
	CachePinger  handler.CachePinger
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
	engine.Use(
		middleware.RequestID(), middleware.SecurityHeaders(), middleware.Logger(log), middleware.Recovery(log),
		middleware.CORS(cfg.CORS), middleware.MaxInFlight(cfg.Server.MaxInFlight, "/healthz", "/readyz"),
		middleware.RequestTimeout(cfg.Server.RequestTimeout), middleware.MaxBodyBytes(cfg.Server.MaxBodyBytes),
	)
	engine.GET("/healthz", handler.HealthHandler(cfg.App))
	engine.GET("/readyz", handler.ReadyHandler(deps.Pinger, deps.CachePinger))
	handler.RegisterOpenAPI(engine)

	auth := middleware.BearerAuth(cfg.Auth)
	api := engine.Group("/api/v1", auth)
	if deps.Projects != nil {
		deps.Projects.Register(api)
	}
	if deps.Documents != nil {
		deps.Documents.Register(api)
	}
	if deps.Repositories != nil {
		deps.Repositories.Register(api)
	}
	if deps.Snapshots != nil {
		deps.Snapshots.Register(api)
	}
	if deps.Search != nil {
		deps.Search.Register(api)
	}
	if deps.Jobs != nil {
		deps.Jobs.Register(api)
	}
	api.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "pong"})
	})
	engine.NoRoute(func(c *gin.Context) {
		if c.Request.URL.Path == "/api/v1" || strings.HasPrefix(c.Request.URL.Path, "/api/v1/") {
			auth(c)
			if c.IsAborted() {
				return
			}
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "not found", "code": "not_found"})
	})
	return engine
}
