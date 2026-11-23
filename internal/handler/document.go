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
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"projectlens/internal/config"
	"projectlens/internal/domain"
)

type DocumentService interface {
	Create(context.Context, string, domain.CreateDocumentInput) (domain.Document, error)
	GetByID(context.Context, string, string) (domain.Document, error)
	ListByProject(context.Context, string, domain.ListDocumentsFilter) (domain.DocumentListResult, error)
	Update(context.Context, string, string, domain.UpdateDocumentInput) (domain.Document, error)
	Patch(context.Context, string, string, domain.PatchDocumentInput) (domain.Document, error)
	Delete(context.Context, string, string, *int64) error
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
	docs := group.Group("/projects/:id/documents")
	docs.POST("", h.create)
	docs.GET("", h.list)
	docs.GET("/:docId", h.get)
	docs.PUT("/:docId", h.update)
	docs.PATCH("/:docId", h.patch)
	docs.DELETE("/:docId", h.delete)
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

func (h *DocumentHandler) create(c *gin.Context) {
	input, err := decodeDocument[domain.CreateDocumentInput](c, h.maxBodyBytes)
	if err != nil {
		h.writeError(c, err)
		return
	}
	doc, err := h.service.Create(c.Request.Context(), c.Param("id"), input)
	h.respond(c, http.StatusCreated, doc, err)
}

func (h *DocumentHandler) list(c *gin.Context) {
	var filter domain.ListDocumentsFilter
	for _, field := range []struct {
		name   string
		target *int
		min    int
		max    int
	}{
		{"limit", &filter.Limit, 1, 100}, {"offset", &filter.Offset, 0, 0},
	} {
		if value, ok := c.GetQueryArray(field.name); ok {
			number, err := strconv.Atoi(value[0])
			if len(value) != 1 || err != nil || number < field.min || (field.max != 0 && number > field.max) {
				h.writeError(c, fmt.Errorf("%w: invalid %s", domain.ErrInvalidInput, field.name))
				return
			}
			*field.target = number
		}
	}
	result, err := h.service.ListByProject(c.Request.Context(), c.Param("id"), filter)
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *DocumentHandler) get(c *gin.Context) {
	doc, err := h.service.GetByID(c.Request.Context(), c.Param("id"), c.Param("docId"))
	h.respond(c, http.StatusOK, doc, err)
}

func (h *DocumentHandler) update(c *gin.Context) {
	input, err := decodeDocument[struct {
		domain.CreateDocumentInput
		Version json.RawMessage `json:"version"`
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
	doc, err := h.service.Update(c.Request.Context(), c.Param("id"), c.Param("docId"),
		domain.UpdateDocumentInput{Title: input.Title, Content: input.Content, Version: version})
	h.respond(c, http.StatusOK, doc, err)
}

func (h *DocumentHandler) patch(c *gin.Context) {
	input, err := decodeDocument[struct {
		Title   *string         `json:"title"`
		Content *string         `json:"content"`
		Version json.RawMessage `json:"version"`
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
	doc, err := h.service.Patch(c.Request.Context(), c.Param("id"), c.Param("docId"),
		domain.PatchDocumentInput{Title: input.Title, Content: input.Content, Version: version})
	h.respond(c, http.StatusOK, doc, err)
}

func (h *DocumentHandler) delete(c *gin.Context) {
	version, err := documentIfMatch(c.Request.Header)
	if err == nil {
		err = h.service.Delete(c.Request.Context(), c.Param("id"), c.Param("docId"), version)
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
