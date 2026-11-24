package handler

import (
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"projectlens/internal/domain"
	"projectlens/internal/gitscan"
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
	for _, route := range []struct {
		path, operation string
		query           []string
	}{
		{"/refs", "refs", []string{"type", "limit", "offset"}},
		{"/commits/:sha/tree", "tree", []string{"path_prefix", "limit", "offset"}},
		{"/commits/:sha/file", "file", []string{"path"}},
		{"/files/history", "history", []string{"path", "ref", "follow", "limit", "offset"}},
		{"/files/blame", "blame", []string{"path", "ref", "start_line", "end_line"}},
	} {
		repo.GET(route.path, func(c *gin.Context) {
			reply := RepositoryHandler{log: h.Log}
			data, err := h.query(c, route.operation, route.query)
			reply.respond(c, http.StatusOK, data, err)
		})
	}
}

func (h *HistoryHandler) query(c *gin.Context, operation string, allowed []string) (any, error) {
	target, err := scopedTarget(c)
	if err != nil {
		return nil, err
	}
	params, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return nil, fmt.Errorf("%w: malformed query parameters", domain.ErrInvalidInput)
	}
	for name, values := range params {
		if len(values) != 1 || !slices.Contains(allowed, name) {
			return nil, fmt.Errorf("%w: unexpected or repeated query parameter %q", domain.ErrInvalidInput, name)
		}
	}
	if slices.Contains(allowed, "path") && params.Get("path") == "" {
		return nil, fmt.Errorf("%w: path is required", domain.ErrInvalidInput)
	}
	limit, err := historyNumber(params, "limit", 20, 1, gitscan.HistoryPage)
	if err != nil {
		return nil, err
	}
	offset, err := historyNumber(params, "offset", 0, 0, math.MaxInt32)
	if err != nil {
		return nil, err
	}
	ctx := c.Request.Context()
	target.Ref = params.Get("ref")
	switch operation {
	case "refs":
		refType := params.Get("type")
		if refType != "" && refType != "branch" && refType != "tag" {
			return nil, fmt.Errorf("%w: type must be branch or tag", domain.ErrInvalidInput)
		}
		return h.Service.ListRefs(ctx, target, refType, limit, offset)
	case "tree":
		return h.Service.TreeAtCommit(ctx, target, c.Param("sha"), params.Get("path_prefix"), limit, offset)
	case "file":
		return h.Service.FileAtCommit(ctx, target, c.Param("sha"), params.Get("path"))
	case "history":
		follow := params.Get("follow")
		if params.Has("follow") && follow != "true" && follow != "false" {
			return nil, fmt.Errorf("%w: follow must be true or false", domain.ErrInvalidInput)
		}
		return h.Service.FileHistory(ctx, target, params.Get("path"), follow == "true", limit, offset)
	case "blame":
		start, err := historyNumber(params, "start_line", 0, 1, math.MaxInt32)
		if err != nil {
			return nil, err
		}
		end, err := historyNumber(params, "end_line", 0, 1, math.MaxInt32)
		if err != nil {
			return nil, err
		}
		if (start == 0) != (end == 0) || end < start || end-start >= gitscan.BlameLineLimit {
			return nil, fmt.Errorf("%w: supply both line bounds for a range of at most %d lines", domain.ErrInvalidInput, gitscan.BlameLineLimit)
		}
		return h.Service.Blame(ctx, target, params.Get("path"), start, end)
	default:
		return nil, fmt.Errorf("unregistered history operation %q", operation)
	}
}

func historyNumber(params url.Values, name string, fallback, lower, upper int) (int, error) {
	if !params.Has(name) {
		return fallback, nil
	}
	raw := params.Get(name)
	n, err := strconv.Atoi(raw)
	if err != nil || n < lower || n > upper ||
		strings.ContainsFunc(raw, func(r rune) bool { return r < '0' || r > '9' }) {
		return 0, fmt.Errorf("%w: invalid %s", domain.ErrInvalidInput, name)
	}
	return n, nil
}
