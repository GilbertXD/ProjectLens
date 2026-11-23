package gitscan

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"projectlens/internal/domain"
	"projectlens/internal/github"
)

type RemoteSource struct {
	cacheDir string
	token    string
	// Tests can route a validated GitHub URL to a hermetic HTTP fixture.
	testHTTPBase string
}

func NewRemoteSource(cacheDir, token string) *RemoteSource {
	if cacheDir == "" {
		cacheDir = filepath.Join(os.TempDir(), "projectlens-git-cache")
	}
	return &RemoteSource{cacheDir: cacheDir, token: token}
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
	ref, err := scanRef(spec.Ref)
	if err != nil {
		return nil, err
	}
	repoPath := "/" + strings.ToLower(owner+"/"+name)
	canonical := "https://github.com" + repoPath
	cloneURL := canonical + ".git"
	git := gitRunner{token: s.token, testHTTP: s.testHTTPBase != ""}
	if git.testHTTP {
		cloneURL = strings.TrimRight(s.testHTTPBase, "/") + repoPath + ".git"
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
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(canonical)))
	dir := filepath.Join(s.cacheDir, key+".git")
	release, err := lockCache(ctx, filepath.Join(s.cacheDir, key+".lock"))
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
		fetchDir, err = os.MkdirTemp(s.cacheDir, key+"-")
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
	}

	// Credentials may grant private access. Verify public visibility without them.
	if s.token != "" && !git.testHTTP {
		if _, err := git.run(ctx, 8192, false, "ls-remote", "--", cloneURL, "HEAD"); err != nil {
			return nil, remoteError(err)
		}
	}
	if err := fetchCommit(ctx, git, fetchDir, cloneURL, ref); err != nil {
		return nil, err
	}
	oid, err := git.resolve(ctx, fetchDir, "FETCH_HEAD")
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: fetched ref does not resolve to a commit", domain.ErrInvalidInput)
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

func fetchCommit(ctx context.Context, git gitRunner, dir, cloneURL, ref string) error {
	args := []string{"--git-dir=" + dir, "fetch", "--no-tags", "--prune", "--no-auto-maintenance"}
	shallow := append(append([]string(nil), args...), "--depth=1", "--", cloneURL, ref)
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
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
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
