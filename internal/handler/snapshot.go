package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"projectlens/internal/domain"
)

type SnapshotService interface {
	ListSnapshots(context.Context, string, string, domain.ListSnapshotsFilter) (domain.SnapshotListResult, error)
	GetActiveSnapshot(context.Context, string, string) (domain.Snapshot, error)
	SearchActiveFiles(context.Context, string, string, domain.SearchFilesFilter) (domain.SourceFileListResult, error)
	GetActiveFile(context.Context, string, string, string) (domain.SourceFile, error)
}

type SnapshotHandler struct {
	service SnapshotService
	log     *slog.Logger
}

func NewSnapshotHandler(service SnapshotService, log *slog.Logger) *SnapshotHandler {
	return &SnapshotHandler{service: service, log: log}
}

func (h *SnapshotHandler) Register(group *gin.RouterGroup) {
	repo := group.Group("/projects/:id/repositories/:repoId")
	repo.GET("/snapshots", h.listSnapshots)
	repo.GET("/snapshots/active", h.activeSnapshot)
	repo.GET("/files", h.searchFiles)
	repo.GET("/files/content", h.fileContent)
}

func readSnapshotPage(c *gin.Context) (limit, offset int, err error) {
	for _, field := range []struct {
		name string
		dest *int
	}{{"limit", &limit}, {"offset", &offset}} {
		if value, ok := c.GetQuery(field.name); ok {
			number, err := strconv.Atoi(value)
			if err != nil || number < 0 || field.name == "limit" && (number == 0 || number > 100) {
				return 0, 0, fmt.Errorf("%w: invalid %s", domain.ErrInvalidInput, field.name)
			}
			*field.dest = number
		}
	}
	return limit, offset, nil
}

func (h *SnapshotHandler) listSnapshots(c *gin.Context) {
	limit, offset, err := readSnapshotPage(c)
	if err != nil {
		h.respond(c, nil, err)
		return
	}
	result, err := h.service.ListSnapshots(c.Request.Context(), c.Param("id"), c.Param("repoId"),
		domain.ListSnapshotsFilter{Limit: limit, Offset: offset})
	h.respond(c, result, err)
}

func (h *SnapshotHandler) activeSnapshot(c *gin.Context) {
	result, err := h.service.GetActiveSnapshot(c.Request.Context(), c.Param("id"), c.Param("repoId"))
	h.respond(c, result, err)
}

func (h *SnapshotHandler) searchFiles(c *gin.Context) {
	limit, offset, err := readSnapshotPage(c)
	if err != nil {
		h.respond(c, nil, err)
		return
	}
	result, err := h.service.SearchActiveFiles(c.Request.Context(), c.Param("id"), c.Param("repoId"),
		domain.SearchFilesFilter{Query: c.Query("q"), PathPrefix: c.Query("path_prefix"), Limit: limit, Offset: offset})
	h.respond(c, result, err)
}

func (h *SnapshotHandler) fileContent(c *gin.Context) {
	path := c.Query("path")
	if path == "" {
		h.respond(c, nil, fmt.Errorf("%w: path is required", domain.ErrInvalidInput))
		return
	}
	result, err := h.service.GetActiveFile(c.Request.Context(), c.Param("id"), c.Param("repoId"), path)
	h.respond(c, result, err)
}

func (h *SnapshotHandler) respond(c *gin.Context, result any, err error) {
	if err != nil && writeRequestLimitError(c, err) {
		return
	}
	switch {
	case err == nil:
		c.JSON(http.StatusOK, result)
	case errors.Is(err, domain.ErrInvalidInput):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": "invalid_input"})
	case errors.Is(err, domain.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "not found", "code": "not_found"})
	default:
		h.log.ErrorContext(c.Request.Context(), "snapshot request failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error", "code": "internal_error"})
	}
}
