package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"projectlens/internal/domain"
)

type Activity struct {
	ID     int64  `json:"id,omitempty"`
	Number int    `json:"number,omitempty"`
	Title  string `json:"title,omitempty"`
	Body   string `json:"body,omitempty"`
	User   struct {
		Login string `json:"login"`
	} `json:"user"`
	State     string    `json:"state,omitempty"`
	CreatedAt time.Time `json:"created_at,omitzero"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	URL       string    `json:"html_url"`
	SHA       string    `json:"sha,omitempty"`
	Path      string    `json:"path,omitempty"`
	Line      int       `json:"line,omitempty"`
	CommitID  string    `json:"commit_id,omitempty"`
	Commits   int       `json:"commits,omitempty"`
	IssueURL  string    `json:"issue_url,omitempty"`
	PullURL   string    `json:"pull_request_url,omitempty"`
	Pull      *struct{} `json:"pull_request,omitempty"`
	Truncated bool      `json:"truncated,omitempty"`
}
type RetryError struct{ Delay time.Duration }

func (e *RetryError) Error() string             { return "GitHub rate limit reached" }
func (e *RetryError) Unwrap() error             { return domain.ErrRateLimited }
func (e *RetryError) RetryAfter() time.Duration { return e.Delay }

type ActivityFeed struct {
	client         *Client
	root           *url.URL
	remaining      int
	bytesRemaining int64
}

func (c *Client) Feed(owner, repo string) (*ActivityFeed, error) {
	if !validRepo(owner, repo) {
		return nil, domain.ErrInvalidInput
	}
	u, err := url.Parse(c.config.APIBaseURL)
	if err != nil {
		return nil, err
	}
	return &ActivityFeed{client: c, root: u.JoinPath("repos", owner, repo), remaining: 256, bytesRemaining: 32 << 20}, nil
}
func (f *ActivityFeed) request(ctx context.Context, path string, q url.Values, etag string, out any) (http.Header, int, error) {
	if f.bytesRemaining <= 0 || f.remaining == 0 {
		return nil, 0, fmt.Errorf("%w: activity request limit exceeded", domain.ErrInvalidInput)
	}
	f.remaining--
	endpoint := f.root.JoinPath(path)
	endpoint.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint.String(), nil)
	if err != nil {
		return nil, 0, domain.ErrInvalidInput
	}
	req.Header.Set("User-Agent", f.client.config.UserAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if token := f.client.config.Token; token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := f.client.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: GitHub request failed", domain.ErrUpstreamUnavailable)
	}
	defer res.Body.Close()
	if res.StatusCode == 304 && etag != "" {
		return res.Header, 304, nil
	}
	if err := activityStatus(res); err != nil {
		return nil, 0, err
	}
	limit := min(int64(4<<20), f.bytesRemaining)
	buf, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	f.bytesRemaining -= int64(len(buf))
	if err != nil || int64(len(buf)) > limit || json.Unmarshal(buf, out) != nil {
		return nil, 0, fmt.Errorf("%w: invalid GitHub response", domain.ErrInvalidInput)
	}
	return res.Header, res.StatusCode, nil
}
func activityStatus(res *http.Response) error {
	limited := res.Header.Get("Retry-After") != "" || res.Header.Get("X-RateLimit-Remaining") == "0"
	if res.StatusCode == 403 && !limited {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		limited = strings.Contains(strings.ToLower(string(body)), "rate limit")
	}
	switch {
	case res.StatusCode == 200:
		return nil
	case res.StatusCode == 429 || res.StatusCode == 403 && limited:
		delay := time.Minute
		raw := res.Header.Get("Retry-After")
		if sec, e := strconv.ParseInt(raw, 10, 32); e == nil && sec >= 0 {
			delay = time.Duration(sec) * time.Second
		} else if at, e := http.ParseTime(raw); e == nil {
			delay = max(0, time.Until(at))
		}
		if sec, e := strconv.ParseInt(res.Header.Get("X-RateLimit-Reset"), 10, 64); e == nil {
			delay = max(delay, time.Until(time.Unix(sec, 0)))
		}
		return &RetryError{Delay: delay}
	case res.StatusCode == 403 || res.StatusCode == 401:
		return domain.ErrForbidden
	case res.StatusCode == 404:
		return domain.ErrNotFound
	case res.StatusCode >= 500:
		return fmt.Errorf("%w: GitHub HTTP %d", domain.ErrUpstreamUnavailable, res.StatusCode)
	default:
		return fmt.Errorf("%w: GitHub HTTP %d", domain.ErrInvalidInput, res.StatusCode)
	}
}
func (a *Activity) normalize() error {
	u, err := url.Parse(a.URL)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") || u.User != nil || len(a.URL) > 2048 {
		return errors.New("invalid GitHub source URL")
	}
	body := boundedText(a.Body, 16384)
	a.Truncated = body != a.Body
	a.Body = body
	a.Title = boundedText(a.Title, 512)
	a.Path = boundedText(a.Path, 1024)
	a.User.Login = boundedText(a.User.Login, 128)
	return nil
}
func (f *ActivityFeed) List(ctx context.Context, path string, q url.Values, visit func(Activity) error) error {
	if q == nil {
		q = url.Values{}
	}
	q.Set("per_page", "100")
	for page := 1; page <= 50; page++ {
		q.Set("page", strconv.Itoa(page))
		var items []Activity
		h, _, err := f.request(ctx, path, q, "", &items)
		if err != nil {
			return err
		}
		if items == nil || len(items) > 100 {
			return fmt.Errorf("%w: invalid GitHub page", domain.ErrInvalidInput)
		}
		for _, item := range items {
			if err := item.normalize(); err != nil {
				return fmt.Errorf("%w: %s", domain.ErrInvalidInput, err)
			}
			if err := visit(item); err != nil {
				return err
			}
		}
		if !strings.Contains(h.Get("Link"), `rel="next"`) {
			return nil
		}
	}
	return fmt.Errorf("%w: GitHub page limit exceeded", domain.ErrInvalidInput)
}
func (f *ActivityFeed) Pull(ctx context.Context, n int, etag string) (Activity, string, bool, error) {
	var a Activity
	h, status, err := f.request(ctx, "pulls/"+strconv.Itoa(n), nil, etag, &a)
	if err != nil {
		return a, "", false, err
	}
	if status == 304 {
		return a, etag, true, nil
	}
	if a.Number != n || a.normalize() != nil {
		return a, "", false, fmt.Errorf("%w: invalid pull request", domain.ErrInvalidInput)
	}
	return a, boundedText(h.Get("ETag"), 256), false, nil
}
