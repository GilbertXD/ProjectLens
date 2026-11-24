package handler

import (
	"log/slog"

	"github.com/gin-gonic/gin"

	"projectlens/internal/domain"
	"projectlens/internal/service"
)

type ScheduleHandler struct {
	Service *service.Scheduler
	Log     *slog.Logger
}

func (h *ScheduleHandler) Register(api *gin.RouterGroup) {
	base := api.Group("/projects/:id/repositories/:repoId/sync-policy")
	run := func(c *gin.Context) {
		reply := RepositoryHandler{log: h.Log}
		target, err := scopedTarget(c)
		if err != nil {
			reply.writeError(c, err)
			return
		}
		var in *domain.PolicyInput
		if c.Request.Method == "PUT" {
			value, err := decodeRepository[domain.PolicyInput](c)
			if err != nil {
				reply.writeError(c, err)
				return
			}
			in = &value
		}
		policies, err := h.Service.Policy(c.Request.Context(), target, c.Param("kind"), in)
		reply.respond(c, 200, gin.H{"items": policies}, err)
	}
	base.GET("", run)
	base.PUT("/:kind", run)
}
