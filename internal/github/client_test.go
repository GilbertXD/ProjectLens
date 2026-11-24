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

func TestGitHubTransportReadOnlyMethods(t *testing.T) {
	calls := 0
	transport := observedTransport{transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req, err := http.NewRequestWithContext(t.Context(), method, "https://api.github.com", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transport.RoundTrip(req); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s = %v, want forbidden", method, err)
		}
	}
	if calls != 0 {
		t.Fatalf("underlying transport received %d write requests", calls)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		req, err := http.NewRequestWithContext(t.Context(), method, "https://api.github.com", nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		_ = res.Body.Close()
	}
	if calls != 2 {
		t.Fatalf("underlying transport received %d read requests", calls)
	}
}

func TestGitHubRedirectStaysOnOrigin(t *testing.T) {
	for _, tc := range []struct {
		location string
		want     error
	}{
		{"/repos/Canonical/Repo", nil},
		{"https://sub.api.example.com/repos/Canonical/Repo", domain.ErrForbidden},
		{"https://other.example.com/repos/Canonical/Repo", domain.ErrForbidden},
		{"http://api.example.com/repos/Canonical/Repo", domain.ErrForbidden},
	} {
		t.Run(tc.location, func(t *testing.T) {
			calls := 0
			transport := transportFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Host != "api.example.com" || req.URL.Scheme != "https" {
					t.Errorf("redirect reached another origin with Authorization present = %t", req.Header.Get("Authorization") != "")
				}
				if req.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("request to the original origin had no token")
				}
				if calls == 1 {
					header := make(http.Header)
					header.Set("Location", tc.location)
					return &http.Response{StatusCode: http.StatusFound, Header: header, Body: http.NoBody}, nil
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(publicMetadata))}, nil
			})
			cfg := config.GitHubConfig{APIBaseURL: "https://api.example.com", Token: "test-token"}
			_, err := NewClient(cfg, &http.Client{Transport: transport}).Fetch(t.Context(), "owner", "repo")
			if !errors.Is(err, tc.want) || (tc.want != nil && calls != 1) || (tc.want == nil && calls != 2) {
				t.Fatalf("Fetch = %v after %d calls, want %v", err, calls, tc.want)
			}
		})
	}
}

func TestGitHubTokenScopes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		token  string
		scopes []string
		want   error
	}{
		{name: "anonymous", scopes: []string{"repo"}},
		{name: "zero scope", token: "fixture", scopes: []string{""}},
		{name: "fine grained", token: "fixture"},
		{name: "read scope", token: "fixture", scopes: []string{"read:org"}},
		{name: "multiple read scopes", token: "fixture", scopes: []string{"read:org, read:user", "read:packages"}},
		{name: "repo", token: "fixture", scopes: []string{"repo"}, want: domain.ErrForbidden},
		{name: "public_repo", token: "fixture", scopes: []string{"public_repo"}, want: domain.ErrForbidden},
		{name: "delete_repo", token: "fixture", scopes: []string{"delete_repo"}, want: domain.ErrForbidden},
		{name: "write:org", token: "fixture", scopes: []string{"read:org, write:org"}, want: domain.ErrForbidden},
		{name: "admin:repo_hook", token: "fixture", scopes: []string{"read:org", "admin:repo_hook"}, want: domain.ErrForbidden},
		{name: "workflow", token: "fixture", scopes: []string{"workflow"}, want: domain.ErrForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := transportFunc(func(req *http.Request) (*http.Response, error) {
				header := make(http.Header)
				for _, scope := range tc.scopes {
					header.Add("X-OAuth-Scopes", scope)
				}
				body := publicMetadata
				if strings.HasSuffix(req.URL.Path, "/issues") {
					body = "[]"
				}
				return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			cfg := config.GitHubConfig{APIBaseURL: "https://api.example.com", Token: tc.token}
			client := NewClient(cfg, &http.Client{Transport: transport})
			if _, err := client.Fetch(t.Context(), "owner", "repo"); !errors.Is(err, tc.want) {
				t.Errorf("Fetch = %v, want %v", err, tc.want)
			}
			feed, err := client.Feed("owner", "repo")
			if err != nil {
				t.Fatal(err)
			}
			var issues []Activity
			if _, _, err := feed.request(t.Context(), "issues", nil, "", &issues); !errors.Is(err, tc.want) {
				t.Errorf("activity = %v, want %v", err, tc.want)
			}
		})
	}
}
