package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"projectlens/internal/domain"
)

type SearchService interface {
	Search(context.Context, string, domain.SearchFilter) (domain.SearchResult, error)
}

type SearchHandler struct {
	service SearchService
	log     *slog.Logger
}

func NewSearchHandler(service SearchService, log *slog.Logger) *SearchHandler {
	return &SearchHandler{service: service, log: log}
}

func (h *SearchHandler) Register(group *gin.RouterGroup) {
	group.GET("/projects/:id/search", h.search)
}

func (h *SearchHandler) search(c *gin.Context) {
	filter, err := readSearchFilter(c.Request.URL.RawQuery)
	var result domain.SearchResult
	if err == nil {
		result, err = h.service.Search(c.Request.Context(), c.Param("id"), filter)
	}
	if err != nil && writeRequestLimitError(c, err) {
		return
	}
	switch {
	case err == nil:
		c.Header("X-Search-Cache", string(result.CacheStatus))
		c.Header("X-Search-Version", strconv.FormatInt(result.SearchVersion, 10))
		c.JSON(http.StatusOK, result)
	case errors.Is(err, domain.ErrInvalidInput):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": "invalid_input"})
	case errors.Is(err, domain.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "not found", "code": "not_found"})
	default:
		h.log.ErrorContext(c.Request.Context(), "search request failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error", "code": "internal_error"})
	}
}

func readSearchFilter(rawQuery string) (domain.SearchFilter, error) {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return domain.SearchFilter{}, fmt.Errorf("%w: malformed query parameters", domain.ErrInvalidInput)
	}
	for _, key := range []string{"q", "repository_id", "path_prefix", "limit", "offset"} {
		if len(values[key]) > 1 {
			return domain.SearchFilter{}, fmt.Errorf("%w: duplicate %s", domain.ErrInvalidInput, key)
		}
	}

	filter := domain.SearchFilter{Query: values.Get("q"), PathPrefix: values.Get("path_prefix")}
	for _, value := range values["source_type"] {
		for _, source := range strings.Split(value, ",") {
			filter.SourceTypes = append(filter.SourceTypes, domain.SearchSourceType(strings.TrimSpace(source)))
		}
	}
	if values.Has("repository_id") {
		id, err := uuid.Parse(values.Get("repository_id"))
		if err != nil {
			return domain.SearchFilter{}, fmt.Errorf("%w: invalid repository_id", domain.ErrInvalidInput)
		}
		filter.RepositoryID = &id
	}
	for _, field := range []struct {
		name string
		dest *int
		min  int
	}{{"limit", &filter.Limit, 1}, {"offset", &filter.Offset, 0}} {
		if values.Has(field.name) {
			number, err := strconv.Atoi(values.Get(field.name))
			if err != nil || number < field.min {
				return domain.SearchFilter{}, fmt.Errorf("%w: invalid %s", domain.ErrInvalidInput, field.name)
			}
			*field.dest = number
		}
	}
	return filter, nil
}
