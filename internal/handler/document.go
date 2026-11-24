package handler

import (
	"bytes"
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
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"projectlens/internal/config"
	"projectlens/internal/domain"
)

type DocumentService interface {
	Create(context.Context, string, domain.CreateDocumentInput) (domain.Document, error)
	GetByID(context.Context, string, string, *uuid.UUID) (domain.Document, error)
	ListByProject(context.Context, string, domain.ListDocumentsFilter) (domain.DocumentListResult, error)
	Update(context.Context, string, string, *uuid.UUID, domain.UpdateDocumentInput) (domain.Document, error)
	Patch(context.Context, string, string, *uuid.UUID, domain.PatchDocumentInput) (domain.Document, error)
	Delete(context.Context, string, string, *uuid.UUID, *int64) error
}

type DocumentHandler struct {
	service      DocumentService
	log          *slog.Logger
	maxBodyBytes int64
}

func NewDocumentHandler(service DocumentService, log *slog.Logger, maxContentBytes int64) *DocumentHandler {
	if maxContentBytes <= 0 {
		maxContentBytes = config.DefaultDocumentMaxContentBytes
	}
	return &DocumentHandler{service: service, log: log, maxBodyBytes: max(256<<10, maxContentBytes*2+65536)}
}

func (h *DocumentHandler) Register(group *gin.RouterGroup) {
	for _, path := range []string{"/projects/:id/documents", "/projects/:id/repositories/:repoId/documents"} {
		docs := group.Group(path)
		docs.POST("", h.withDocumentScope(h.create))
		docs.GET("", h.withDocumentScope(h.list))
		docs.GET("/:docId", h.withDocumentScope(h.get))
		docs.PUT("/:docId", h.withDocumentScope(h.update))
		docs.PATCH("/:docId", h.withDocumentScope(h.patch))
		docs.DELETE("/:docId", h.withDocumentScope(h.delete))
	}
}

func (h *DocumentHandler) withDocumentScope(next func(*gin.Context, *uuid.UUID)) gin.HandlerFunc {
	return func(c *gin.Context) {
		var repositoryID *uuid.UUID
		if raw := c.Param("repoId"); raw != "" {
			id, err := uuid.Parse(raw)
			if err != nil {
				h.writeError(c, fmt.Errorf("%w: invalid repository ID", domain.ErrInvalidInput))
				return
			}
			repositoryID = &id
		}
		next(c, repositoryID)
	}
}

func decodeDocument[T any](c *gin.Context, maxBodyBytes int64) (T, error) {
	var input *T
	var zero T
	body := http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		if cause := requestLimitCause(c, err); cause != nil {
			return zero, cause
		}
		return zero, fmt.Errorf("%w: cannot read request body", domain.ErrInvalidInput)
	}
	if !utf8.Valid(data) {
		return zero, fmt.Errorf("%w: body must be valid UTF-8", domain.ErrInvalidInput)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input == nil {
		return zero, fmt.Errorf("%w: body must be a valid document JSON object", domain.ErrInvalidInput)
	}
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return zero, fmt.Errorf("%w: body must contain a single JSON value", domain.ErrInvalidInput)
	}
	return *input, nil
}

func documentIfMatch(header http.Header) (*int64, error) {
	values := header.Values("If-Match")
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) == 1 {
		value := strings.TrimSpace(values[0])
		if strings.HasPrefix(value, `W/"`) {
			value = value[2:]
		}
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			value = value[1 : len(value)-1]
		}
		if value != "" && strings.Trim(value, "0123456789") == "" {
			if version, err := strconv.ParseInt(value, 10, 64); err == nil && version >= 1 {
				return &version, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: If-Match must contain one positive integer version", domain.ErrInvalidInput)
}

func documentWriteVersion(header http.Header, body json.RawMessage) (int64, error) {
	ifMatch, err := documentIfMatch(header)
	if err != nil {
		return 0, err
	}
	if body == nil {
		if ifMatch != nil {
			return *ifMatch, nil
		}
		return 0, fmt.Errorf("%w: version or If-Match is required", domain.ErrInvalidInput)
	}
	var version int64
	if err := json.Unmarshal(body, &version); err != nil || version < 1 {
		return 0, fmt.Errorf("%w: version must be a positive integer", domain.ErrInvalidInput)
	}
	if ifMatch != nil && *ifMatch != version {
		return 0, fmt.Errorf("%w: version and If-Match must agree", domain.ErrInvalidInput)
	}
	return version, nil
}

func (h *DocumentHandler) create(c *gin.Context, repositoryID *uuid.UUID) {
	input, err := decodeDocument[domain.CreateDocumentInput](c, h.maxBodyBytes)
	if err != nil {
		h.writeError(c, err)
		return
	}
	if repositoryID != nil {
		if input.RepositoryID != nil && *input.RepositoryID != *repositoryID {
			h.writeError(c, fmt.Errorf("%w: repository_id must match the repository in the path", domain.ErrInvalidInput))
			return
		}
		input.RepositoryID = repositoryID
	}
	doc, err := h.service.Create(c.Request.Context(), c.Param("id"), input)
	h.respond(c, http.StatusCreated, doc, err)
}

func (h *DocumentHandler) list(c *gin.Context, repositoryID *uuid.UUID) {
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		h.writeError(c, fmt.Errorf("%w: malformed query parameters", domain.ErrInvalidInput))
		return
	}
	filter := domain.ListDocumentsFilter{RepositoryID: repositoryID}
	if values := query["scope"]; len(values) > 0 {
		scope := domain.KnowledgeScope(values[0])
		if len(values) != 1 || (scope != domain.KnowledgeScopeProject && scope != domain.KnowledgeScopeRepository) {
			h.writeError(c, fmt.Errorf("%w: scope must be project or repository", domain.ErrInvalidInput))
			return
		}
		filter.Scope = scope
	}
	if values := query["repository_id"]; len(values) > 0 {
		id, err := uuid.Parse(values[0])
		if len(values) != 1 || err != nil || (repositoryID != nil && *repositoryID != id) {
			h.writeError(c, fmt.Errorf("%w: invalid repository_id for this document list", domain.ErrInvalidInput))
			return
		}
		filter.RepositoryID = &id
	}
	if filter.Scope == domain.KnowledgeScopeProject && filter.RepositoryID != nil {
		h.writeError(c, fmt.Errorf("%w: project scope cannot be combined with repository_id", domain.ErrInvalidInput))
		return
	}
	for _, field := range []struct {
		name   string
		target *int
		min    int
		max    int
	}{
		{"limit", &filter.Limit, 1, 100}, {"offset", &filter.Offset, 0, 0},
	} {
		if value := query[field.name]; len(value) > 0 {
			number, err := strconv.Atoi(value[0])
			if len(value) != 1 || err != nil || number < field.min || (field.max != 0 && number > field.max) {
				h.writeError(c, fmt.Errorf("%w: invalid %s", domain.ErrInvalidInput, field.name))
				return
			}
			*field.target = number
		}
	}
	if repositoryID != nil {
		filter.Scope = domain.KnowledgeScopeRepository
	}
	result, err := h.service.ListByProject(c.Request.Context(), c.Param("id"), filter)
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *DocumentHandler) get(c *gin.Context, repositoryID *uuid.UUID) {
	doc, err := h.service.GetByID(c.Request.Context(), c.Param("id"), c.Param("docId"), repositoryID)
	h.respond(c, http.StatusOK, doc, err)
}

func (h *DocumentHandler) update(c *gin.Context, repositoryID *uuid.UUID) {
	input, err := decodeDocument[struct {
		Title   string                 `json:"title"`
		Content string                 `json:"content"`
		Version json.RawMessage        `json:"version"`
		Origin  *domain.DocumentOrigin `json:"origin"`
	}](c, h.maxBodyBytes)
	if err != nil {
		h.writeError(c, err)
		return
	}
	version, err := documentWriteVersion(c.Request.Header, input.Version)
	if err != nil {
		h.writeError(c, err)
		return
	}
	doc, err := h.service.Update(c.Request.Context(), c.Param("id"), c.Param("docId"), repositoryID,
		domain.UpdateDocumentInput{Title: input.Title, Content: input.Content, Version: version, Origin: input.Origin})
	h.respond(c, http.StatusOK, doc, err)
}

// documentOriginPatch reads the tri-state PATCH origin value. An absent field
// leaves the stored provenance untouched, null clears it, and an object
// replaces it.
func documentOriginPatch(raw json.RawMessage) (*domain.DocumentOrigin, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	if string(bytes.TrimSpace(raw)) == "null" {
		return nil, true, nil
	}
	var origin *domain.DocumentOrigin
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&origin); err != nil || origin == nil {
		return nil, false, fmt.Errorf("%w: origin must be null or an object with repository_id and repository_name", domain.ErrInvalidInput)
	}
	return origin, true, nil
}

func (h *DocumentHandler) patch(c *gin.Context, repositoryID *uuid.UUID) {
	input, err := decodeDocument[struct {
		Title   *string         `json:"title"`
		Content *string         `json:"content"`
		Version json.RawMessage `json:"version"`
		Origin  json.RawMessage `json:"origin"`
	}](c, h.maxBodyBytes)
	if err != nil {
		h.writeError(c, err)
		return
	}
	version, err := documentWriteVersion(c.Request.Header, input.Version)
	if err != nil {
		h.writeError(c, err)
		return
	}
	origin, originTouched, err := documentOriginPatch(input.Origin)
	if err != nil {
		h.writeError(c, err)
		return
	}
	doc, err := h.service.Patch(c.Request.Context(), c.Param("id"), c.Param("docId"), repositoryID,
		domain.PatchDocumentInput{
			Title: input.Title, Content: input.Content, Version: version,
			Origin: origin, ClearOrigin: originTouched && origin == nil,
		})
	h.respond(c, http.StatusOK, doc, err)
}

func (h *DocumentHandler) delete(c *gin.Context, repositoryID *uuid.UUID) {
	version, err := documentIfMatch(c.Request.Header)
	if err == nil {
		err = h.service.Delete(c.Request.Context(), c.Param("id"), c.Param("docId"), repositoryID, version)
	}
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *DocumentHandler) respond(c *gin.Context, status int, doc domain.Document, err error) {
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.Header("ETag", strconv.Quote(strconv.FormatInt(doc.Version, 10)))
	c.JSON(status, doc)
}

func (h *DocumentHandler) writeError(c *gin.Context, err error) {
	if writeRequestLimitError(c, err) {
		return
	}
	switch {
	case errors.Is(err, domain.ErrPayloadTooLarge):
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": domain.ErrPayloadTooLarge.Error(), "code": "payload_too_large"})
	case errors.Is(err, domain.ErrInvalidInput):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": "invalid_input"})
	case errors.Is(err, domain.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error(), "code": "not_found"})
	case errors.Is(err, domain.ErrVersionConflict):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "code": "version_conflict"})
	default:
		h.log.ErrorContext(c.Request.Context(), "document request failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error", "code": "internal_error"})
	}
}
