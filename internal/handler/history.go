package handler

import (
	"fmt"
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"projectlens/internal/domain"
	"projectlens/internal/service"
)

type HistoryHandler struct {
	Service *service.HistoryService
	Log     *slog.Logger
}

func scopedTarget(c *gin.Context) (domain.SyncTarget, error) {
	project, e1 := uuid.Parse(c.Param("id"))
	repo, e2 := uuid.Parse(c.Param("repoId"))
	if e1 != nil || e2 != nil {
		return domain.SyncTarget{}, fmt.Errorf("%w: expected project and repository UUIDs", domain.ErrInvalidInput)
	}
	return domain.SyncTarget{ProjectID: project, RepositoryID: repo, Ref: c.Query("ref")}, nil
}
func (h *HistoryHandler) Register(group *gin.RouterGroup) {
	repo := group.Group("/projects/:id/repositories/:repoId")
	for path, op := range map[string]string{"/commits": "list", "/commits/:sha": "detail", "/commits/:sha/files": "files", "/compare": "compare"} {
		repo.GET(path, func(c *gin.Context) {
			reply := RepositoryHandler{log: h.Log}
			target, err := scopedTarget(c)
			if err != nil {
				reply.writeError(c, err)
				return
			}
			limit, offset, err := readSnapshotPage(c)
			if err != nil {
				reply.writeError(c, err)
				return
			}
			if limit == 0 {
				limit = 20
			}
			sha := c.Param("sha")
			if op == "compare" {
				sha = c.Query("head")
			}
			data, err := h.Service.Read(c.Request.Context(), target, op, sha, c.Query("base"), limit, offset)
			reply.respond(c, 200, data, err)
		})
	}
}
