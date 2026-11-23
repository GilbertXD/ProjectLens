package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"projectlens/internal/domain"
	"projectlens/internal/middleware"
)

type ProjectService interface {
	Create(context.Context, domain.CreateProjectInput) (domain.Project, error)
	GetByID(context.Context, string) (domain.Project, error)
	List(context.Context, domain.ListProjectsFilter) (domain.ProjectListResult, error)
	Update(context.Context, string, domain.UpdateProjectInput) (domain.Project, error)
	Patch(context.Context, string, domain.PatchProjectInput) (domain.Project, error)
	Delete(context.Context, string) error
}

type ProjectHandler struct {
	service ProjectService
	log     *slog.Logger
}

func NewProjectHandler(service ProjectService, log *slog.Logger) *ProjectHandler {
	return &ProjectHandler{service: service, log: log}
}

func (h *ProjectHandler) Register(group *gin.RouterGroup) {
	group.POST("/projects", h.create)
	group.GET("/projects", h.list)
	group.GET("/projects/:id", h.get)
	group.PUT("/projects/:id", h.update)
	group.PATCH("/projects/:id", h.patch)
	group.DELETE("/projects/:id", h.delete)
}

func decodeProject[T any](c *gin.Context) (T, error) {
	var input *T
	var zero T
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input == nil {
		if cause := requestLimitCause(c, err); cause != nil {
			return zero, cause
		}
		return zero, fmt.Errorf("%w: body must be a valid project JSON object", domain.ErrInvalidInput)
	}
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		if cause := requestLimitCause(c, err); cause != nil {
			return zero, cause
		}
		return zero, fmt.Errorf("%w: body must contain a single JSON value", domain.ErrInvalidInput)
	}
	return *input, nil
}

func (h *ProjectHandler) create(c *gin.Context) {
	input, err := decodeProject[domain.CreateProjectInput](c)
	if err != nil {
		h.writeError(c, err)
		return
	}
	project, err := h.service.Create(c.Request.Context(), input)
	h.respond(c, http.StatusCreated, project, err)
}

func (h *ProjectHandler) get(c *gin.Context) {
	project, err := h.service.GetByID(c.Request.Context(), c.Param("id"))
	h.respond(c, http.StatusOK, project, err)
}

func (h *ProjectHandler) list(c *gin.Context) {
	filter := domain.ListProjectsFilter{Status: domain.ProjectStatus(c.Query("status"))}
	if value, ok := c.GetQuery("status"); ok && value == "" {
		h.writeError(c, fmt.Errorf("%w: status must be active or archived", domain.ErrInvalidInput))
		return
	}
	for _, field := range []struct {
		name   string
		target *int
	}{
		{"limit", &filter.Limit}, {"offset", &filter.Offset},
	} {
		if value, ok := c.GetQuery(field.name); ok {
			number, err := strconv.Atoi(value)
			if err != nil || (field.name == "limit" && number == 0) {
				h.writeError(c, fmt.Errorf("%w: invalid %s", domain.ErrInvalidInput, field.name))
				return
			}
			*field.target = number
		}
	}
	result, err := h.service.List(c.Request.Context(), filter)
	h.respond(c, http.StatusOK, result, err)
}

func (h *ProjectHandler) update(c *gin.Context) {
	input, err := decodeProject[domain.UpdateProjectInput](c)
	if err != nil {
		h.writeError(c, err)
		return
	}
	project, err := h.service.Update(c.Request.Context(), c.Param("id"), input)
	h.respond(c, http.StatusOK, project, err)
}

func (h *ProjectHandler) patch(c *gin.Context) {
	input, err := decodeProject[domain.PatchProjectInput](c)
	if err != nil {
		h.writeError(c, err)
		return
	}
	project, err := h.service.Patch(c.Request.Context(), c.Param("id"), input)
	h.respond(c, http.StatusOK, project, err)
}

func (h *ProjectHandler) delete(c *gin.Context) {
	if err := h.service.Delete(c.Request.Context(), c.Param("id")); err != nil {
		h.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *ProjectHandler) respond(c *gin.Context, status int, body any, err error) {
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(status, body)
}

func (h *ProjectHandler) writeError(c *gin.Context, err error) {
	if writeRequestLimitError(c, err) {
		return
	}
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": "invalid_input"})
	case errors.Is(err, domain.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": domain.ErrNotFound.Error(), "code": "not_found"})
	case errors.Is(err, domain.ErrConflict):
		c.JSON(http.StatusConflict, gin.H{"error": domain.ErrConflict.Error(), "code": "conflict"})
	default:
		h.log.ErrorContext(c.Request.Context(), "project request failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error", "code": "internal_error"})
	}
}

// Preserve limit and timeout errors before replacing JSON decoding errors with
// generic messages that cannot echo user-supplied body values.
func requestLimitCause(c *gin.Context, err error) error {
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		return tooLarge
	case errors.Is(err, context.DeadlineExceeded), errors.Is(c.Request.Context().Err(), context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func writeRequestLimitError(c *gin.Context, err error) bool {
	switch cause := requestLimitCause(c, err); cause {
	case nil:
		return false
	case context.DeadlineExceeded:
		c.JSON(http.StatusGatewayTimeout, gin.H{"error": "request timed out", "code": "request_timeout", "request_id": middleware.GetRequestID(c)})
	default:
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "payload too large", "code": "payload_too_large", "request_id": middleware.GetRequestID(c)})
	}
	return true
}
