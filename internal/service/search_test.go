package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"

	"projectlens/internal/config"
	"projectlens/internal/domain"
)

type searchTestStore struct {
	version, queryVersion int64
	lookupErr, searchErr  error
	reads, searches       int
}

func (s *searchTestStore) GetProjectSearchVersion(context.Context, uuid.UUID) (int64, error) {
	s.reads++
	return s.version, s.lookupErr
}

func (s *searchTestStore) Search(_ context.Context, _ uuid.UUID, f domain.SearchFilter) (domain.SearchResult, error) {
	s.searches++
	version := s.version
	if s.queryVersion != 0 {
		version = s.queryVersion
	}
	return domain.SearchResult{Query: f.Query, Limit: f.Limit, Offset: f.Offset,
		Items: []domain.SearchHit{}, SearchVersion: version}, s.searchErr
}

type searchTestCache struct {
	data           map[string][]byte
	getErr, setErr error
	gets, sets     int
	key            string
	ttl            time.Duration
}

func (c *searchTestCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	c.gets++
	c.key = key
	p, ok := c.data[key]
	return p, ok, c.getErr
}

func (c *searchTestCache) Set(_ context.Context, key string, p []byte, ttl time.Duration) error {
	c.sets++
	c.ttl = ttl
	if c.setErr != nil {
		return c.setErr
	}
	c.data[key] = p
	return nil
}

func TestSearchFilterValidation(t *testing.T) {
	store := &searchTestStore{version: 1}
	cfg := config.SearchConfig{DefaultLimit: 3, MaxLimit: 5, MaxQueryLength: 2, CacheTTL: time.Minute}
	svc := NewSearchService(store, nil, cfg, "test", nil)
	id := uuid.NewString()
	if _, err := svc.Search(t.Context(), "invalid", domain.SearchFilter{Query: "ok"}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("invalid project ID: %v", err)
	}
	for _, f := range []domain.SearchFilter{
		{Query: " "}, {Query: "a\x00"}, {Query: "\xff"}, {Query: "搜索词"},
		{Query: "ok", SourceTypes: []domain.SearchSourceType{"unknown"}},
		{Query: "ok", PathPrefix: "\xff"}, {Query: "ok", PathPrefix: "\x00"},
		{Query: "ok", Limit: -1}, {Query: "ok", Limit: 6}, {Query: "ok", Offset: -1},
	} {
		if _, err := svc.Search(t.Context(), id, f); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("filter %+v: %v", f, err)
		}
	}
	if store.reads != 0 {
		t.Fatal("invalid filters reached PostgreSQL")
	}
	result, err := svc.Search(t.Context(), id, domain.SearchFilter{Query: " 搜索 "})
	if err != nil || result.Query != "搜索" || result.Limit != 3 || result.CacheStatus != domain.SearchCacheStatusBypass {
		t.Fatalf("validated search = %+v, %v", result, err)
	}
}

func TestSearchCacheKeysAndInvalidation(t *testing.T) {
	store := &searchTestStore{version: 17}
	cache := &searchTestCache{data: make(map[string][]byte)}
	cfg := config.DefaultSearchConfig()
	svc := NewSearchService(store, cache, cfg, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	project := uuid.NewString()
	f := domain.SearchFilter{Query: " café ", SourceTypes: []domain.SearchSourceType{
		domain.SearchSourceLocalGit, domain.SearchSourceGitHub, domain.SearchSourceLocalGit,
	}}
	result, err := svc.Search(t.Context(), project, f)
	if err != nil || result.CacheStatus != domain.SearchCacheStatusMiss || result.SearchVersion != 17 || cache.ttl != cfg.CacheTTL {
		t.Fatalf("first search = %+v, %v, TTL = %s", result, err, cache.ttl)
	}
	key := cache.key
	if !regexp.MustCompile("^test:search:v1:p:" + project + ":ver:17:q:[a-f0-9]{64}$").MatchString(key) {
		t.Fatalf("unexpected key %q", key)
	}
	f = domain.SearchFilter{Query: "café", Limit: cfg.DefaultLimit, SourceTypes: []domain.SearchSourceType{
		domain.SearchSourceGitHub, domain.SearchSourceLocalGit,
	}}
	result, err = svc.Search(t.Context(), project, f)
	if err != nil || result.CacheStatus != domain.SearchCacheStatusHit || cache.key != key || store.searches != 1 || store.reads != 2 {
		t.Fatalf("canonical search = %+v, %v, key = %q, store = %+v", result, err, cache.key, store)
	}
	repoID := uuid.New()
	for _, change := range []func(*domain.SearchFilter){
		func(q *domain.SearchFilter) { q.Query += " fi" },
		func(q *domain.SearchFilter) { q.SourceTypes = q.SourceTypes[:1] },
		func(q *domain.SearchFilter) { q.RepositoryID = &repoID },
		func(q *domain.SearchFilter) { q.PathPrefix = "src/" },
		func(q *domain.SearchFilter) { q.Limit++ },
		func(q *domain.SearchFilter) { q.Offset++ },
	} {
		filter := f
		change(&filter)
		result, err := svc.Search(t.Context(), project, filter)
		if err != nil || result.CacheStatus != domain.SearchCacheStatusMiss || cache.key == key {
			t.Errorf("changed filter %+v returned %+v, %v", filter, result, err)
		}
	}
	if result, err = svc.Search(t.Context(), uuid.NewString(), f); err != nil || result.CacheStatus != domain.SearchCacheStatusMiss || cache.key == key {
		t.Fatalf("other project search = %+v, %v", result, err)
	}
	store.version++
	result, err = svc.Search(t.Context(), project, f)
	if err != nil || result.CacheStatus != domain.SearchCacheStatusMiss || result.SearchVersion != 18 || cache.key == key {
		t.Fatalf("new version search = %+v, %v", result, err)
	}

	gets := cache.gets
	store.lookupErr = domain.ErrNotFound
	if _, err = svc.Search(t.Context(), project, f); !errors.Is(err, domain.ErrNotFound) || cache.gets != gets {
		t.Fatalf("missing project accessed the cache or returned the wrong error: %v", err)
	}
}

func TestSearchCacheDegradationAndVersionRace(t *testing.T) {
	offline := errors.New("offline")
	for _, tc := range []struct {
		name           string
		getErr, setErr error
		payload        []byte
		queryVersion   int64
		wantStatus     domain.SearchCacheStatus
		wantSets       int
	}{
		{name: "read error", getErr: offline, wantStatus: domain.SearchCacheStatusDegraded},
		{name: "write error", setErr: offline, wantStatus: domain.SearchCacheStatusDegraded, wantSets: 1},
		{name: "malformed JSON", payload: []byte("["), wantStatus: domain.SearchCacheStatusDegraded},
		{name: "invalid payload", payload: []byte("null"), wantStatus: domain.SearchCacheStatusDegraded},
		{name: "version changed", queryVersion: 3, wantStatus: domain.SearchCacheStatusMiss},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := uuid.New()
			cfg := config.DefaultSearchConfig()
			f := domain.SearchFilter{Query: "test", Limit: cfg.DefaultLimit}
			store := &searchTestStore{version: 2, queryVersion: tc.queryVersion}
			cache := &searchTestCache{data: make(map[string][]byte), getErr: tc.getErr, setErr: tc.setErr}
			if tc.payload != nil {
				cache.data[searchCacheKey("test", id, store.version, f)] = tc.payload
			}
			svc := NewSearchService(store, cache, cfg, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
			result, err := svc.Search(t.Context(), id.String(), f)
			if err != nil || result.Items == nil || result.CacheStatus != tc.wantStatus ||
				result.SearchVersion != max(store.version, store.queryVersion) || cache.sets != tc.wantSets || store.searches != 1 {
				t.Fatalf("result = %+v, %v, writes = %d, store = %+v", result, err, cache.sets, store)
			}
			store.searchErr = offline
			if _, err := svc.Search(t.Context(), id.String(), f); !errors.Is(err, offline) {
				t.Fatalf("unexpected SQL error: %v", err)
			}
		})
	}
}
