package github

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"projectlens/internal/domain"
)

var ownerPattern = regexp.MustCompile(`^[A-Za-z0-9]+(-[A-Za-z0-9]+)*$`)
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

func validRepo(owner, repo string) bool {
	return len(owner) <= 39 && ownerPattern.MatchString(owner) &&
		repoPattern.MatchString(repo) && repo != "." && repo != ".."
}

func ParsePublicRepoURL(raw string) (owner, repo, cleanURL string, err error) {
	invalid := fmt.Errorf("%w: source must be an HTTPS GitHub repository URL", domain.ErrInvalidInput)
	raw = strings.TrimSpace(raw)
	if strings.ContainsFunc(raw, unicode.IsControl) || strings.ContainsAny(raw, "?#") {
		return "", "", "", invalid
	}
	target, err := url.Parse(raw)
	if err != nil || target.Scheme != "https" || target.User != nil ||
		(!strings.EqualFold(target.Host, "github.com") && !strings.EqualFold(target.Host, "www.github.com")) {
		return "", "", "", invalid
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(target.EscapedPath(), "/"), "/"), "/")
	if len(parts) != 2 {
		return "", "", "", invalid
	}
	owner, repo = parts[0], parts[1]
	if strings.HasSuffix(strings.ToLower(repo), ".git") {
		repo = repo[:len(repo)-4]
	}
	if !validRepo(owner, repo) {
		return "", "", "", invalid
	}
	return owner, repo, "https://github.com/" + owner + "/" + repo, nil
}
