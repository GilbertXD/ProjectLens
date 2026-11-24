package handler

import (
	"fmt"
	"log/slog"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"projectlens/internal/domain"
	"projectlens/internal/repository"
)

type ActivityHandler struct {
	Store *repository.PostgresRepositoryStore
	Log   *slog.Logger
}

func (h *ActivityHandler) Register(api *gin.RouterGroup) {
	project := api.Group("/projects/:id")
	for _, resource := range []struct{ path, kind string }{{"/issues", "issue"}, {"/pulls", "pull"}} {
		for _, prefix := range []string{"", "/repositories/:repoId"} {
			project.GET(prefix+resource.path, h.read(resource.kind, "list"))
		}
		root := "/repositories/:repoId" + resource.path + "/:number"
		project.GET(root, h.read(resource.kind, "detail"))
		project.GET(root+"/comments", h.read(resource.kind, "comments"))
		if resource.kind == "pull" {
			project.GET(root+"/commits", h.read(resource.kind, "commits"))
		}
	}
}
func (h *ActivityHandler) read(kind, op string) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		reply := RepositoryHandler{log: h.Log}
		project, err := uuid.Parse(c.Param("id"))
		if err != nil {
			reply.writeError(c, domain.ErrInvalidInput)
			return
		}
		var repo *uuid.UUID
		if raw := c.Param("repoId"); raw != "" {
			id, err := uuid.Parse(raw)
			if err != nil {
				reply.writeError(c, domain.ErrInvalidInput)
				return
			}
			repo = &id
			if _, err := h.Store.GetByID(ctx, project, id); err != nil {
				reply.writeError(c, err)
				return
			}
		}
		limit, offset, err := readSnapshotPage(c)
		if err != nil {
			reply.writeError(c, err)
			return
		}
		if limit == 0 {
			limit = 20
		}
		kinds := []string{kind}
		var n int
		if op != "list" {
			v, err := strconv.Atoi(c.Param("number"))
			if err != nil || v < 1 || v > 2147483647 {
				reply.writeError(c, fmt.Errorf("%w: invalid resource number", domain.ErrInvalidInput))
				return
			}
			n = v
			parent, err := h.Store.ActivityParent(ctx, project, *repo, kind, n)
			if err != nil || op == "detail" {
				reply.respond(c, 200, parent, err)
				return
			}
			if op == "comments" {
				kinds = []string{"comment"}
				if kind == "pull" {
					kinds = append(kinds, "review_comment")
				}
			} else {
				kinds = []string{"commit"}
			}
		}
		items, err := h.Store.ReadActivity(ctx, project, repo, kinds, n, limit, offset)
		reply.respond(c, 200, gin.H{"items": items, "limit": limit, "offset": offset}, err)
	}
}
