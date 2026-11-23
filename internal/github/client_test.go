package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"projectlens/internal/config"
	"projectlens/internal/domain"
)

func TestParsePublicRepoURL(t *testing.T) {
	for _, raw := range []string{"https://github.com/Some-Owner/Repo_1", "  https://WWW.GITHUB.COM/Some-Owner/Repo_1.GIT/  "} {
		owner, repo, clean, err := ParsePublicRepoURL(raw)
		if err != nil || owner != "Some-Owner" || repo != "Repo_1" || clean != "https://github.com/Some-Owner/Repo_1" {
			t.Fatalf("parse %q = %q %q %q %v", raw, owner, repo, clean, err)
		}
	}
	for _, raw := range []string{
		"", "owner/repo", "git@github.com:owner/repo", "ssh://github.com/owner/repo", "file:///owner/repo",
		"http://github.com/owner/repo", "https://github.com.evil/owner/repo", "https://github.com:443/owner/repo",
		"https://user@github.com/owner/repo", "https://github.com/owner/repo?", "https://github.com/owner/repo#",
		"https://github.com/owner/repo/issues", "https://github.com/owner/repo//", "https://github.com//owner/repo",
		"https://github.com/-owner/repo", "https://github.com/owner-/repo", "https://github.com/own--er/repo",
		"https://github.com/" + strings.Repeat("a", 40) + "/repo", "https://github.com/owner/" + strings.Repeat("a", 101),
		"https://github.com/owner/..", "https://github.com/owner/.", "https://github.com/owner/.git",
		"https://github.com/owner/a%2fb", "https://github.com/owner/a%00", "https://github.com/owner/a\x00",
	} {
		if _, _, _, err := ParsePublicRepoURL(raw); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("parse %q = %v, want invalid input", raw, err)
		}
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

const publicMetadata = `{"id":17,"full_name":"Canonical/Repo","owner":{"login":"ignored"},"name":"ignored","visibility":"public","stargazers_count":5,"language":"Go","default_branch":"main","description":"A repository"}`

func TestGitHubFetch(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		status                 int
		body, token, remaining string
		want                   error
	}{
		{name: "public", status: 200, body: publicMetadata},
		{name: "token and owner fallback", status: 200, body: `{"id":17,"owner":{"login":"Canonical"},"name":"Repo","stargazers_count":5,"language":"Go","default_branch":"main","description":"A repository"}`, token: "secret"},
		{name: "private", status: 200, body: `{"private":true}`, want: domain.ErrForbidden},
		{name: "internal", status: 200, body: `{"visibility":"internal"}`, want: domain.ErrForbidden},
		{name: "missing", status: 404, want: domain.ErrNotFound},
		{name: "rate limit", status: 403, remaining: "0", want: domain.ErrRateLimited},
		{name: "too many requests", status: 429, want: domain.ErrRateLimited},
		{name: "unauthorized", status: 401, want: domain.ErrUpstreamUnavailable},
		{name: "forbidden", status: 403, want: domain.ErrUpstreamUnavailable},
		{name: "server error", status: 503, want: domain.ErrUpstreamUnavailable},
		{name: "malformed", status: 200, body: "{", want: domain.ErrUpstreamUnavailable},
		{name: "oversize", status: 200, body: publicMetadata + strings.Repeat(" ", 1<<20), want: domain.ErrUpstreamUnavailable},
		{name: "missing identity", status: 200, body: "{}", want: domain.ErrUpstreamUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := transportFunc(func(req *http.Request) (*http.Response, error) {
				authorization := ""
				if tc.token != "" {
					authorization = "Bearer " + tc.token
				}
				if req.Method != "GET" || req.URL.String() != "https://api.example.com/base/repos/Old/Name" ||
					req.Header.Get("Accept") != "application/vnd.github+json" || req.Header.Get("X-GitHub-Api-Version") != "2022-11-28" ||
					req.Header.Get("User-Agent") != "ProjectLens" || req.Header.Get("Authorization") != authorization {
					t.Error("unexpected GitHub request")
				}
				headers := make(http.Header)
				headers.Set("X-RateLimit-Remaining", tc.remaining)
				return &http.Response{StatusCode: tc.status, Header: headers, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			cfg := config.GitHubConfig{APIBaseURL: "https://api.example.com/base/", UserAgent: "ProjectLens", Token: tc.token}
			got, err := NewClient(cfg, &http.Client{Transport: transport}).Fetch(t.Context(), "Old", "Name")
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if tc.want == nil && (got.ID != 17 || got.Owner != "Canonical" || got.Repo != "Repo" ||
				got.CanonicalSource != "github:canonical/repo" || got.Visibility != "public" || got.Stars != 5 ||
				got.Language == nil || *got.Language != "Go" || got.DefaultBranch != "main" ||
				got.Description != "A repository" || got.RemoteURL != "https://github.com/Canonical/Repo") {
				t.Fatalf("unexpected metadata: %+v", got)
			}
		})
	}
}

func TestGitHubTimeout(t *testing.T) {
	transport := transportFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, context.DeadlineExceeded
	})
	cfg := config.GitHubConfig{APIBaseURL: "https://api.example.com", Timeout: 10 * time.Millisecond}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := NewClient(cfg, &http.Client{Transport: transport}).Fetch(ctx, "owner", "repo"); !errors.Is(err, domain.ErrUpstreamUnavailable) || ctx.Err() != nil {
		t.Fatalf("client timeout returned %v (parent context: %v)", err, ctx.Err())
	}
}

func TestGitHubFetchSanitizesMetadata(t *testing.T) {
	repo := strings.Repeat("r", 100)
	fallback := "https://github.com/Canonical/" + repo
	for _, tc := range []struct {
		name, htmlURL, wantURL string
	}{
		{"valid URL", "https://www.github.com/Canonical/" + repo, "https://www.github.com/Canonical/" + repo},
		{"empty URL", "", fallback},
		{"invalid URL", "https://user:secret@github.com/Canonical/" + repo, fallback},
		{"NUL in URL", fallback + "\x00", fallback},
		{"oversized URL", fallback + strings.Repeat("r", 2048), fallback},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{
				"id": 17, "full_name": "Canonical/" + repo, "html_url": tc.htmlURL,
				"description":    "\x00" + strings.Repeat("界", 2005),
				"default_branch": "topic/\x00" + strings.Repeat("界", 256),
				"language":       "G\x00o",
			})
			if err != nil {
				t.Fatal(err)
			}
			transport := transportFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})
			cfg := config.GitHubConfig{APIBaseURL: "https://api.example.com"}
			got, err := NewClient(cfg, &http.Client{Transport: transport}).Fetch(t.Context(), "owner", "repo")
			if err != nil || got.RemoteURL != tc.wantURL || got.Repo != repo ||
				got.Description != strings.Repeat("界", 2000) || got.DefaultBranch != "topic/"+strings.Repeat("界", 249) ||
				!utf8.ValidString(got.Description) || !utf8.ValidString(got.DefaultBranch) || got.Language == nil || *got.Language != "Go" {
				t.Fatalf("unexpected sanitized metadata: repo=%q url=%q description runes=%d branch runes=%d error=%v",
					got.Repo, got.RemoteURL, utf8.RuneCountInString(got.Description), utf8.RuneCountInString(got.DefaultBranch), err)
			}
		})
	}
}
