package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"projectlens/internal/config"
	"projectlens/internal/domain"
)

type Metadata struct {
	Owner           string
	Repo            string
	ID              int64
	Visibility      string
	Stars           int
	Language        *string
	DefaultBranch   string
	Description     string
	RemoteURL       string
	CanonicalSource string
}

type Client struct {
	config config.GitHubConfig
	http   *http.Client
}

func NewClient(cfg config.GitHubConfig, client *http.Client) *Client {
	if client == nil {
		client = &http.Client{}
	}
	configured := *client
	if cfg.Timeout > 0 {
		configured.Timeout = cfg.Timeout
	}
	return &Client{config: cfg, http: &configured}
}

func (c *Client) Fetch(ctx context.Context, owner, repo string) (Metadata, error) {
	if !validRepo(owner, repo) {
		return Metadata{}, fmt.Errorf("%w: invalid GitHub repository name", domain.ErrInvalidInput)
	}
	endpoint, err := url.Parse(c.config.APIBaseURL)
	if err != nil {
		return Metadata{}, fmt.Errorf("%w: invalid GitHub API endpoint", domain.ErrUpstreamUnavailable)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.JoinPath("repos", owner, repo).String(), nil)
	if err != nil {
		return Metadata{}, fmt.Errorf("%w: cannot build GitHub request", domain.ErrUpstreamUnavailable)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", c.config.UserAgent)
	if c.config.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.config.Token)
	}

	res, err := c.http.Do(req)
	if err != nil {
		return Metadata{}, fmt.Errorf("%w: GitHub request failed", domain.ErrUpstreamUnavailable)
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound:
		return Metadata{}, fmt.Errorf("%w: GitHub repository not found", domain.ErrNotFound)
	case res.StatusCode == http.StatusTooManyRequests ||
		(res.StatusCode == http.StatusForbidden && res.Header.Get("X-RateLimit-Remaining") == "0"):
		return Metadata{}, fmt.Errorf("%w: GitHub rate limit reached", domain.ErrRateLimited)
	case res.StatusCode != http.StatusOK:
		return Metadata{}, fmt.Errorf("%w: GitHub returned HTTP %d", domain.ErrUpstreamUnavailable, res.StatusCode)
	}

	var data struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		FullName string `json:"full_name"`
		Owner    struct {
			Login string `json:"login"`
		} `json:"owner"`
		Private       bool    `json:"private"`
		Visibility    string  `json:"visibility"`
		Stars         int     `json:"stargazers_count"`
		Language      *string `json:"language"`
		DefaultBranch string  `json:"default_branch"`
		Description   string  `json:"description"`
		HTMLURL       string  `json:"html_url"`
	}
	const maxBody = 1 << 20
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil || len(body) > maxBody || json.Unmarshal(body, &data) != nil {
		return Metadata{}, fmt.Errorf("%w: invalid GitHub response", domain.ErrUpstreamUnavailable)
	}
	if data.Private || (data.Visibility != "" && data.Visibility != "public") {
		return Metadata{}, fmt.Errorf("%w: GitHub repository must be public", domain.ErrForbidden)
	}
	owner, repo = data.Owner.Login, data.Name
	if data.FullName != "" {
		owner, repo, _ = strings.Cut(data.FullName, "/")
	}
	if data.ID <= 0 || !validRepo(owner, repo) {
		return Metadata{}, fmt.Errorf("%w: invalid GitHub repository identity", domain.ErrUpstreamUnavailable)
	}
	remoteURL := strings.TrimSpace(data.HTMLURL)
	if _, _, _, err := ParsePublicRepoURL(remoteURL); err != nil {
		// The validated GitHub URL grammar bounds the fallback and the accepted URL.
		remoteURL = "https://github.com/" + owner + "/" + repo
	}
	if data.Language != nil {
		language := strings.ReplaceAll(*data.Language, "\x00", "")
		data.Language = &language
	}
	return Metadata{
		Owner: owner, Repo: repo, ID: data.ID, Visibility: "public", Stars: data.Stars,
		Language: data.Language, DefaultBranch: boundedText(data.DefaultBranch, 255), Description: boundedText(data.Description, 2000),
		RemoteURL:       remoteURL,
		CanonicalSource: "github:" + strings.ToLower(owner+"/"+repo),
	}, nil
}

// PostgreSQL text rejects NUL and its length checks count characters.
func boundedText(value string, limit int) string {
	value = strings.ReplaceAll(value, "\x00", "")
	for index := range value {
		if limit == 0 {
			return value[:index]
		}
		limit--
	}
	return value
}
