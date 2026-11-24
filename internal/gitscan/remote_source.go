package gitscan

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"projectlens/internal/domain"
	"projectlens/internal/github"
)

type RemoteSource struct {
	cacheDir   string
	token      string
	gitBaseURL string
}

func NewRemoteSource(cacheDir, token, gitBaseURL string) *RemoteSource {
	if cacheDir == "" {
		cacheDir = filepath.Join(os.TempDir(), "projectlens-git-cache")
	}
	return &RemoteSource{cacheDir: cacheDir, token: token, gitBaseURL: gitBaseURL}
}

// Lock the same cache entry across goroutines and separate worker processes.
func lockCache(ctx context.Context, path string) (func(), error) {
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			_ = syscall.Close(fd)
			return nil, err
		}
		err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return sync.OnceFunc(func() { _ = syscall.Close(fd) }), nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			_ = syscall.Close(fd)
			return nil, err
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

func (s *RemoteSource) Prepare(ctx context.Context, spec SourceSpec) (*PreparedRepo, error) {
	owner, name, _, err := github.ParsePublicRepoURL(spec.Source)
	if err != nil {
		return nil, err
	}
	ref, err := NormalizeRef(spec.Ref)
	if err != nil {
		return nil, err
	}
	repoPath := "/" + strings.ToLower(owner+"/"+name)
	canonical := "https://github.com" + repoPath
	cloneURL := canonical + ".git"
	git := gitRunner{token: s.token}
	if s.gitBaseURL != "" {
		base, err := url.Parse(s.gitBaseURL)
		if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Hostname() == "" {
			return nil, errors.New("invalid Git mirror base URL")
		}
		cloneURL = base.JoinPath(repoPath + ".git").String()
		// GitHub credentials are scoped to github.com. Mirrors receive no token.
		git = gitRunner{allowHTTP: base.Scheme == "http"}
	}
	if err := os.MkdirAll(s.cacheDir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(s.cacheDir)
	if err != nil {
		return nil, err
	}
	ownerInfo, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm()&0022 != 0 || !ok || ownerInfo.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("Git cache must be a directory owned by the worker and writable only by its owner")
	}
	cacheDir, err := filepath.Abs(s.cacheDir)
	if err != nil {
		return nil, err
	}
	cacheDir, err = filepath.EvalSymlinks(cacheDir)
	if err != nil {
		return nil, err
	}
	git.cacheDir = cacheDir
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(canonical)))
	dir := filepath.Join(cacheDir, key+".git")
	release, err := lockCache(ctx, filepath.Join(cacheDir, key+".lock"))
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			release()
		}
	}()

	fetchDir := dir
	info, err = os.Lstat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		fetchDir, err = os.MkdirTemp(cacheDir, key+"-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(fetchDir)
		if _, err := git.run(ctx, 8192, false, "init", "--bare", "--template=", fetchDir); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	case !info.IsDir():
		return nil, errors.New("Git cache entry is not a directory")
	case spec.compareBase != "":
		// A comparison may resolve both commits without going through fetch.
		if err := validateCacheTree(dir); err != nil {
			return nil, err
		}
	}

	// Credentials may grant private access. Verify public visibility without them.
	if git.token != "" {
		if _, err := git.run(ctx, 8192, false, "ls-remote", "--", cloneURL, "HEAD"); err != nil {
			return nil, remoteError(err)
		}
	}
	var oid string
	if spec.compareBase != "" {
		oid, err = prepareCompare(ctx, git, fetchDir, cloneURL, ref, spec.compareBase)
		if err != nil {
			return nil, err
		}
	} else {
		if err := fetchCommit(ctx, git, fetchDir, cloneURL, ref, max(1, spec.depth)); err != nil {
			return nil, err
		}
		oid, err = git.resolve(ctx, fetchDir, "FETCH_HEAD")
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("%w: fetched ref does not resolve to a commit", domain.ErrInvalidInput)
		}
	}
	if fetchDir != dir {
		if err := os.Rename(fetchDir, dir); err != nil {
			return nil, err
		}
	}
	success = true
	return &PreparedRepo{
		SourceType: domain.RepositorySourceGitHub, Source: spec.Source, CanonicalSource: canonical,
		GitDir: dir, PinnedCommit: oid, ResolvedRef: ref, Release: release,
	}, nil
}

func prepareCompare(ctx context.Context, git gitRunner, dir, cloneURL, head, base string) (string, error) {
	fetchedHead := false
	for _, sha := range []string{head, base} {
		if _, err := git.resolve(ctx, dir, sha); err == nil {
			continue
		}
		if err := fetchCommit(ctx, git, dir, cloneURL, sha, 1); err != nil {
			if fetchedHead {
				break
			}
			if err := fetchCommit(ctx, git, dir, cloneURL, "HEAD", HistoryLimit+2); err != nil {
				return "", err
			}
			fetchedHead = true
		}
	}
	oid, headErr := git.resolve(ctx, dir, head)
	_, baseErr := git.resolve(ctx, dir, base)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if headErr != nil || baseErr != nil {
		return "", fmt.Errorf("%w: compare revisions must resolve to commits", domain.ErrInvalidInput)
	}
	return oid, nil
}

func fetchCommit(ctx context.Context, git gitRunner, dir, cloneURL, ref string, depth int) error {
	args := []string{"--git-dir=" + dir, "fetch", "--no-tags", "--prune", "--no-auto-maintenance"}
	shallow := append(append([]string(nil), args...), "--depth="+strconv.Itoa(depth), "--", cloneURL, ref)
	_, err := git.run(ctx, 8192, true, shallow...)
	if err == nil {
		return nil
	}
	detail := gitDiagnostic(err)
	unsupportedShallow := strings.Contains(detail, "shallow") &&
		(strings.Contains(detail, "support") || strings.Contains(detail, "capable"))
	rejectedSpecificObject := ref != "HEAD" &&
		(strings.Contains(detail, "unadvertised object") || strings.Contains(detail, "not our ref"))
	if unsupportedShallow || rejectedSpecificObject {
		_, err = git.run(ctx, 8192, true, append(args, "--", cloneURL, ref)...)
	}
	return remoteError(err)
}

func gitDiagnostic(err error) string {
	var failure *gitError
	if errors.As(err, &failure) {
		return strings.ToLower(failure.diagnostic)
	}
	return ""
}

func remoteError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, domain.ErrForbidden) {
		return err
	}
	detail := gitDiagnostic(err)
	switch {
	case strings.Contains(detail, "couldn't find remote ref"), strings.Contains(detail, "not our ref"),
		strings.Contains(detail, "unadvertised object"), strings.Contains(detail, "invalid refspec"):
		return fmt.Errorf("%w: remote Git ref is unavailable", domain.ErrInvalidInput)
	case strings.Contains(detail, "repository not found"), strings.Contains(detail, "error: 404"):
		return fmt.Errorf("%w: remote Git repository is unavailable", domain.ErrNotFound)
	case strings.Contains(detail, "error: 429"), strings.Contains(detail, "http 429"),
		strings.Contains(detail, "too many requests"), strings.Contains(detail, "rate limit"),
		strings.Contains(detail, "rate-limit"):
		return fmt.Errorf("%w: remote Git request rate limited", domain.ErrRateLimited)
	case strings.Contains(detail, "could not read username"), strings.Contains(detail, "authentication failed"),
		strings.Contains(detail, "error: 401"), strings.Contains(detail, "error: 403"):
		return fmt.Errorf("%w: remote Git access denied", domain.ErrForbidden)
	default:
		return fmt.Errorf("%w: %v", domain.ErrUpstreamUnavailable, err)
	}
}
