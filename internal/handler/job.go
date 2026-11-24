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
	"strings"

	"github.com/gin-gonic/gin"

	"projectlens/internal/domain"
)

type JobService interface {
	Enqueue(context.Context, domain.CreateJobInput) (domain.Job, bool, error)
	List(context.Context, domain.ListJobsFilter) (domain.JobListResult, error)
	GetByID(context.Context, string) (domain.Job, error)
	Cancel(context.Context, string) (domain.Job, error)
}

type JobHandler struct {
	service JobService
	log     *slog.Logger
}

func NewJobHandler(service JobService, log *slog.Logger) *JobHandler {
	return &JobHandler{service: service, log: log}
}

func (h *JobHandler) Register(api *gin.RouterGroup) {
	api.POST("/projects/:id/repositories/:repoId/sync", h.syncRepository)
	jobs := api.Group("/jobs")
	jobs.POST("", h.enqueue)
	jobs.GET("", h.list)
	jobs.GET("/:id", h.get)
	jobs.POST("/:id/cancel", h.cancel)
}

func (h *JobHandler) enqueue(c *gin.Context) {
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var input *domain.CreateJobInput
	if err := decoder.Decode(&input); err != nil || input == nil {
		if writeRequestLimitError(c, err) {
			return
		}
		h.respond(c, 0, nil, fmt.Errorf("%w: body must be a valid job JSON object", domain.ErrInvalidInput))
		return
	}
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		if writeRequestLimitError(c, err) {
			return
		}
		h.respond(c, 0, nil, fmt.Errorf("%w: body must contain a single JSON value", domain.ErrInvalidInput))
		return
	}
	job, created, err := h.service.Enqueue(c.Request.Context(), *input)
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	h.respond(c, status, job, err)
}

func (h *JobHandler) syncRepository(c *gin.Context) {
	var ref string
	if c.Request.ContentLength != 0 {
		input, err := decodeRepository[struct {
			Ref string `json:"ref"`
		}](c)
		if err != nil {
			h.respond(c, 0, nil, err)
			return
		}
		ref = input.Ref
	}
	payload, _ := json.Marshal(gin.H{"project_id": c.Param("id"), "repository_id": c.Param("repoId"), "ref": ref})
	job, created, err := h.service.Enqueue(c.Request.Context(), domain.CreateJobInput{Type: "repository.sync", Payload: payload})
	if err != nil {
		h.respond(c, 0, nil, err)
		return
	}
	url := "/api/v1/jobs/" + job.ID.String()
	c.Header("Location", url)
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	c.JSON(status, gin.H{"job_id": job.ID, "status": job.Status, "poll_url": url})
}

func (h *JobHandler) list(c *gin.Context) {
	filter := domain.ListJobsFilter{Status: domain.JobStatus(c.Query("status")), Type: c.Query("type")}
	for _, name := range []string{"status", "type"} {
		if raw, ok := c.GetQuery(name); ok && raw == "" {
			h.respond(c, 0, nil, fmt.Errorf("%w: invalid %s", domain.ErrInvalidInput, name))
			return
		}
	}
	for _, field := range []struct {
		name   string
		target *int
	}{{"limit", &filter.Limit}, {"offset", &filter.Offset}} {
		if raw, ok := c.GetQuery(field.name); ok {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 0 || (field.name == "limit" && value == 0) {
				h.respond(c, 0, nil, fmt.Errorf("%w: invalid %s", domain.ErrInvalidInput, field.name))
				return
			}
			*field.target = value
		}
	}
	result, err := h.service.List(c.Request.Context(), filter)
	h.respond(c, http.StatusOK, result, err)
}

func (h *JobHandler) get(c *gin.Context) {
	job, err := h.service.GetByID(c.Request.Context(), c.Param("id"))
	h.respond(c, http.StatusOK, job, err)
}

func (h *JobHandler) cancel(c *gin.Context) {
	job, err := h.service.Cancel(c.Request.Context(), c.Param("id"))
	h.respond(c, http.StatusOK, job, err)
}

func (h *JobHandler) respond(c *gin.Context, status int, body any, err error) {
	if err == nil {
		c.JSON(status, body)
		return
	}
	if writeRequestLimitError(c, err) {
		return
	}
	for _, mapping := range []struct {
		err     error
		status  int
		code    string
		message string
	}{
		{domain.ErrInvalidInput, http.StatusBadRequest, "invalid_input", "invalid input"},
		{domain.ErrNotFound, http.StatusNotFound, "not_found", "job not found"},
		{domain.ErrConflict, http.StatusConflict, "conflict", "job conflict"},
	} {
		if errors.Is(err, mapping.err) {
			message := mapping.message
			if reason, ok := strings.CutPrefix(err.Error(), mapping.err.Error()+": "); ok {
				message = reason
			}
			c.JSON(mapping.status, gin.H{"error": message, "code": mapping.code})
			return
		}
	}
	h.log.ErrorContext(c.Request.Context(), "job request failed", "error", err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error", "code": "internal_error"})
}
