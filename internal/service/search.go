package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"projectlens/internal/config"
	"projectlens/internal/domain"
	"projectlens/internal/telemetry"
)

type SearchStore interface {
	GetProjectSearchVersion(context.Context, uuid.UUID) (int64, error)
	Search(context.Context, uuid.UUID, domain.SearchFilter) (domain.SearchResult, error)
}

type searchRepositoryOwnershipValidator interface {
	ValidateRepositoryOwnership(context.Context, uuid.UUID, []uuid.UUID) error
}

type SearchCache interface {
	Get(context.Context, string) ([]byte, bool, error)
	Set(context.Context, string, []byte, time.Duration) error
}

type SearchService struct {
	store     SearchStore
	cache     SearchCache
	cfg       config.SearchConfig
	keyPrefix string
	log       *slog.Logger
}

func NewSearchService(store SearchStore, cache SearchCache, cfg config.SearchConfig, keyPrefix string, log *slog.Logger) *SearchService {
	if log == nil {
		log = slog.Default()
	}
	return &SearchService{store: store, cache: cache, cfg: cfg, keyPrefix: keyPrefix, log: log}
}

func (s *SearchService) Search(ctx context.Context, rawProjectID string, filter domain.SearchFilter) (out domain.SearchResult, retErr error) {
	start := time.Now()
	ctx, span := telemetry.Span(ctx, "search")
	defer span.End()
	defer func() {
		label := "error"
		if retErr == nil {
			label = string(out.CacheStatus)
		}
		telemetry.Search.WithLabelValues(label).Observe(time.Since(start).Seconds())
	}()
	projectID, err := parseID(rawProjectID)
	if err != nil {
		return domain.SearchResult{}, err
	}
	filter, err = s.normalizeFilter(filter)
	if err != nil {
		return domain.SearchResult{}, err
	}

	// Read the version before validating ownership so a concurrent deletion cannot
	// leave a validated repository paired with a newer cache version.
	version, err := s.store.GetProjectSearchVersion(ctx, projectID)
	if err != nil {
		return domain.SearchResult{}, err
	}

	if validator, ok := s.store.(searchRepositoryOwnershipValidator); ok {
		requested := filter.RepositoryIDs
		if filter.RepositoryID != nil {
			requested = []uuid.UUID{*filter.RepositoryID}
		}
		if len(requested) > 0 {
			if err := validator.ValidateRepositoryOwnership(ctx, projectID, requested); err != nil {
				return domain.SearchResult{}, err
			}
		}
	}

	status := domain.SearchCacheStatusBypass
	var key string
	if s.cache != nil {
		status = domain.SearchCacheStatusMiss
		key = searchCacheKey(s.keyPrefix, projectID, version, filter)
		payload, found, err := s.cache.Get(ctx, key)
		if err == nil && found {
			var cached domain.SearchResult
			err = json.Unmarshal(payload, &cached)
			if err == nil && cached.Items != nil && cached.Query == filter.Query && cached.Limit == filter.Limit &&
				cached.Offset == filter.Offset && cached.SearchVersion == version {
				cached.CacheStatus = domain.SearchCacheStatusHit
				return cached, nil
			}
			if err == nil {
				err = errors.New("cached search response does not match the request")
			}
		}
		if err != nil {
			s.log.WarnContext(ctx, "read search cache", "error", err)
			status = domain.SearchCacheStatusDegraded
		}
	}

	result, err := s.store.Search(ctx, projectID, filter)
	if err != nil {
		return domain.SearchResult{}, err
	}
	// A mutation may have committed between the version lookup and the search.
	// The search statement reads both its results and their version in one snapshot.
	if status == domain.SearchCacheStatusMiss && result.SearchVersion == version {
		payload, err := json.Marshal(result)
		if err == nil {
			err = s.cache.Set(ctx, key, payload, s.cfg.CacheTTL)
		}
		if err != nil {
			s.log.WarnContext(ctx, "write search cache", "error", err)
			status = domain.SearchCacheStatusDegraded
		}
	}
	result.CacheStatus = status
	return result, nil
}

func (s *SearchService) normalizeFilter(filter domain.SearchFilter) (domain.SearchFilter, error) {
	filter.Query = strings.TrimSpace(filter.Query)
	if filter.Query == "" || !utf8.ValidString(filter.Query) || strings.ContainsRune(filter.Query, 0) ||
		utf8.RuneCountInString(filter.Query) > s.cfg.MaxQueryLength {
		return filter, fmt.Errorf("%w: q must be nonblank UTF-8 with no NUL and at most %d characters",
			domain.ErrInvalidInput, s.cfg.MaxQueryLength)
	}
	if !utf8.ValidString(filter.PathPrefix) || strings.ContainsRune(filter.PathPrefix, 0) {
		return filter, fmt.Errorf("%w: path_prefix must be UTF-8 without NUL", domain.ErrInvalidInput)
	}
	if filter.Limit == 0 {
		filter.Limit = s.cfg.DefaultLimit
	}
	if filter.Limit < 1 || filter.Limit > s.cfg.MaxLimit || filter.Offset < 0 {
		return filter, fmt.Errorf("%w: limit must be between 1 and %d and offset must be nonnegative",
			domain.ErrInvalidInput, s.cfg.MaxLimit)
	}
	for _, source := range filter.SourceTypes {
		switch source {
		case domain.SearchSourceLocalGit, domain.SearchSourceGitHub, domain.SearchSourceDocument:
		default:
			return filter, fmt.Errorf("%w: invalid source_type %q", domain.ErrInvalidInput, source)
		}
	}
	if filter.RepositoryRole != "" && !filter.RepositoryRole.Valid() {
		return filter, fmt.Errorf("%w: repository_role must be primary or reference", domain.ErrInvalidInput)
	}
	filter.Scope = filter.EffectiveScope()
	switch filter.Scope {
	case domain.SearchScopeAll:
	case domain.SearchScopeProject:
		if filter.RepositoryID != nil || len(filter.RepositoryIDs) > 0 || filter.RepositoryRole != "" {
			return filter, fmt.Errorf("%w: project scope cannot be combined with repository filters", domain.ErrInvalidInput)
		}
	case domain.SearchScopeRepository:
		if filter.RepositoryID == nil || len(filter.RepositoryIDs) > 0 || filter.RepositoryRole != "" {
			return filter, fmt.Errorf("%w: repository scope requires repository_id and disallows repository_ids and repository_role", domain.ErrInvalidInput)
		}
	case domain.SearchScopeCrossRepository:
		if filter.RepositoryID != nil {
			return filter, fmt.Errorf("%w: cross_repository scope cannot be combined with repository_id", domain.ErrInvalidInput)
		}
	default:
		return filter, fmt.Errorf("%w: invalid scope %q", domain.ErrInvalidInput, filter.Scope)
	}
	if filter.Scope != domain.SearchScopeCrossRepository {
		filter.IncludeProjectDocuments = false
	}
	filter.RepositoryIDs = slices.Clone(filter.RepositoryIDs)
	slices.SortFunc(filter.RepositoryIDs, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	filter.RepositoryIDs = slices.Compact(filter.RepositoryIDs)
	if len(filter.RepositoryIDs) == 0 {
		filter.RepositoryIDs = nil
	}
	filter.SourceTypes = slices.Clone(filter.SourceTypes)
	slices.Sort(filter.SourceTypes)
	filter.SourceTypes = slices.Compact(filter.SourceTypes)
	if len(filter.SourceTypes) == 0 {
		filter.SourceTypes = nil
	}
	return filter, nil
}

func searchCacheKey(prefix string, projectID uuid.UUID, version int64, filter domain.SearchFilter) string {
	canonical, _ := json.Marshal(filter)
	return fmt.Sprintf("%s:search:v2:p:%s:ver:%d:q:%x", prefix, projectID, version, sha256.Sum256(canonical))
}
