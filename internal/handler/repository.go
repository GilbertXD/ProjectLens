package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"projectlens/internal/domain"
)

type RepositoryService interface {
	Associate(context.Context, string, domain.AssociateRepositoryInput) (domain.Repository, error)
	ListByProject(context.Context, string, domain.ListRepositoriesFilter) (domain.RepositoryListResult, error)
	GetByID(context.Context, string, string) (domain.Repository, error)
	Patch(context.Context, string, string, domain.PatchRepositoryInput) (domain.Repository, error)
	Sync(context.Context, string, string) (domain.Repository, error)
	Delete(context.Context, string, string) error
}

type RepositoryHandler struct {
	service RepositoryService
	log     *slog.Logger
}

func NewRepositoryHandler(service RepositoryService, log *slog.Logger) *RepositoryHandler {
	return &RepositoryHandler{service: service, log: log}
}

func (h *RepositoryHandler) Register(group *gin.RouterGroup) {
	repos := group.Group("/projects/:id/repositories")
	repos.POST("", h.associate)
	repos.GET("", h.list)
	repos.GET("/:repoId", h.get)
	repos.PATCH("/:repoId", h.patch)
	repos.POST("/:repoId/refresh-metadata", h.sync)
	repos.DELETE("/:repoId", h.delete)
}

func decodeRepository[T any](c *gin.Context) (T, error) {
	var input *T
	var zero T
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input == nil {
		if cause := requestLimitCause(c, err); cause != nil {
			return zero, cause
		}
		return zero, fmt.Errorf("%w: body must be a valid repository JSON object", domain.ErrInvalidInput)
	}
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		if cause := requestLimitCause(c, err); cause != nil {
			return zero, cause
		}
		return zero, fmt.Errorf("%w: body must contain a single JSON value", domain.ErrInvalidInput)
	}
	return *input, nil
}

func (h *RepositoryHandler) associate(c *gin.Context) {
	input, err := decodeRepository[domain.AssociateRepositoryInput](c)
	if err != nil {
		h.writeError(c, err)
		return
	}
	repo, err := h.service.Associate(c.Request.Context(), c.Param("id"), input)
	h.respond(c, http.StatusCreated, repo, err)
}

func (h *RepositoryHandler) list(c *gin.Context) {
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		h.writeError(c, fmt.Errorf("%w: malformed query parameters", domain.ErrInvalidInput))
		return
	}
	filter := domain.ListRepositoriesFilter{SourceType: domain.RepositorySourceType(query.Get("source_type"))}
	if query.Has("source_type") && filter.SourceType == "" {
		h.writeError(c, fmt.Errorf("%w: source_type must be local_git or github", domain.ErrInvalidInput))
		return
	}
	if values := query["role"]; len(values) > 0 {
		if len(values) != 1 || !domain.RepositoryRole(values[0]).Valid() {
			h.writeError(c, fmt.Errorf("%w: role must be primary or reference", domain.ErrInvalidInput))
			return
		}
		filter.Role = domain.RepositoryRole(values[0])
	}
	for _, field := range []struct {
		name   string
		target *int
	}{{"limit", &filter.Limit}, {"offset", &filter.Offset}} {
		if query.Has(field.name) {
			value, err := strconv.Atoi(query.Get(field.name))
			if err != nil || (field.name == "limit" && value == 0) {
				h.writeError(c, fmt.Errorf("%w: invalid %s", domain.ErrInvalidInput, field.name))
				return
			}
			*field.target = value
		}
	}
	result, err := h.service.ListByProject(c.Request.Context(), c.Param("id"), filter)
	h.respond(c, http.StatusOK, result, err)
}

func (h *RepositoryHandler) get(c *gin.Context) {
	repo, err := h.service.GetByID(c.Request.Context(), c.Param("id"), c.Param("repoId"))
	h.respond(c, http.StatusOK, repo, err)
}

func (h *RepositoryHandler) patch(c *gin.Context) {
	input, err := decodeRepository[domain.PatchRepositoryInput](c)
	if err != nil {
		h.writeError(c, err)
		return
	}
	repo, err := h.service.Patch(c.Request.Context(), c.Param("id"), c.Param("repoId"), input)
	h.respond(c, http.StatusOK, repo, err)
}

func (h *RepositoryHandler) sync(c *gin.Context) {
	repo, err := h.service.Sync(c.Request.Context(), c.Param("id"), c.Param("repoId"))
	h.respond(c, http.StatusOK, repo, err)
}

func (h *RepositoryHandler) delete(c *gin.Context) {
	if err := h.service.Delete(c.Request.Context(), c.Param("id"), c.Param("repoId")); err != nil {
		h.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *RepositoryHandler) respond(c *gin.Context, status int, body any, err error) {
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(status, body)
}

func (h *RepositoryHandler) writeError(c *gin.Context, err error) {
	if writeRequestLimitError(c, err) {
		return
	}
	for _, mapping := range []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrInvalidInput, http.StatusBadRequest, "invalid_input"},
		{domain.ErrForbidden, http.StatusForbidden, "forbidden"},
		{domain.ErrNotFound, http.StatusNotFound, "not_found"},
		{domain.ErrConflict, http.StatusConflict, "conflict"},
		{domain.ErrPayloadTooLarge, http.StatusRequestEntityTooLarge, "payload_too_large"},
		{domain.ErrRateLimited, http.StatusTooManyRequests, "rate_limited"},
		{domain.ErrUpstreamUnavailable, http.StatusBadGateway, "upstream_error"},
	} {
		if errors.Is(err, mapping.err) {
			message := mapping.err.Error()
			switch mapping.err {
			case domain.ErrConflict:
				message = "repository is already associated with this project"
			case domain.ErrInvalidInput, domain.ErrForbidden, domain.ErrNotFound:
				if reason, ok := strings.CutPrefix(err.Error(), message+": "); ok && reason != "" {
					message = reason
				}
			}
			c.JSON(mapping.status, gin.H{"error": message, "code": mapping.code})
			return
		}
	}
	h.log.ErrorContext(c.Request.Context(), "repository request failed", "error", err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error", "code": "internal_error"})
}
