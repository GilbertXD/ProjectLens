package gitscan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"projectlens/internal/domain"
	"projectlens/internal/gitrepo"
	"projectlens/internal/telemetry"
)

type gitRunner struct {
	token     string
	allowHTTP bool
	cacheDir  string
	objectDir string
}

func (g gitRunner) command(ctx context.Context, auth bool, args ...string) *exec.Cmd {
	if err := g.validateCommand(args); err != nil {
		return &exec.Cmd{Err: err}
	}
	fetch := len(args) > 1 && args[1] == "fetch"
	protocol := ""
	if fetch || args[0] == "ls-remote" {
		protocol = g.protocol()
	}
	cmd := gitrepo.GitCommand(ctx, protocol, append([]string{"-c", "http.followRedirects=false"}, args...)...)
	// All commands use explicit Git directories, absolute init paths or URLs.
	// Avoid picking up the application working directory's Git config.
	cmd.Dir = "/"
	cmd.Env = append(cmd.Env, "GIT_LITERAL_PATHSPECS=1")
	if g.objectDir != "" && len(args) > 1 && args[1] == "blame" {
		cmd.Env = append(cmd.Env, "GIT_OBJECT_DIRECTORY="+g.objectDir)
	}
	if fetch && auth && g.token != "" && !g.allowHTTP {
		cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://github.com/.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Bearer "+g.token)
	}
	return cmd
}

func (g gitRunner) protocol() string {
	if g.allowHTTP {
		return "http"
	}
	return "https"
}

func (g gitRunner) remoteURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == g.protocol() && u.Hostname() != "" && u.User == nil
}

// validateCommand permits only the command forms used by the scanner. Read
// commands can otherwise accept options that write output or run diff drivers.
func (g gitRunner) validateCommand(args []string) error {
	forbidden := fmt.Errorf("%w: Git command is outside the scanner capability", domain.ErrForbidden)
	if len(args) == 0 {
		return forbidden
	}
	switch args[0] {
	case "init":
		if len(args) == 4 && slices.Equal(args[1:3], []string{"--bare", "--template="}) && g.inCache(args[3], true) {
			return nil
		}
	case "ls-remote":
		if len(args) == 4 && args[1] == "--" && g.remoteURL(args[2]) && args[3] == "HEAD" {
			return nil
		}
		if len(args) == 7 && args[1] == "--symref" && args[2] == "--" && g.remoteURL(args[3]) &&
			slices.Equal(args[4:], []string{"HEAD", "refs/heads/*", "refs/tags/*"}) {
			return nil
		}
	default:
		dir, ok := strings.CutPrefix(args[0], "--git-dir=")
		if !ok || !filepath.IsAbs(dir) || len(args) < 2 {
			return forbidden
		}
		if args[1] == "fetch" {
			if g.validFetch(args[2:]) && g.inCache(dir, false) {
				return nil
			}
		} else if readOnlyCommand(args[1], args[2:]) {
			return nil
		}
	}
	return forbidden
}

func (g gitRunner) inCache(dir string, allowMissing bool) bool {
	if g.cacheDir == "" || !filepath.IsAbs(dir) || dir != filepath.Clean(dir) {
		return false
	}
	root, err := filepath.EvalSymlinks(g.cacheDir)
	if err != nil || !filepath.IsAbs(root) {
		return false
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(dir))
	if err != nil {
		return false
	}
	target := filepath.Join(parent, filepath.Base(dir))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return false
	}
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return allowMissing
	}
	if err != nil || !info.IsDir() {
		return false
	}
	if allowMissing {
		entries, err := os.ReadDir(target)
		return err == nil && len(entries) == 0
	}
	return validateCacheTree(target) == nil
}

// Cache entries are standalone bare repositories. Git follows metadata paths
// when fetching, so refuse entries that can redirect a write outside the cache.
func validateCacheTree(dir string) error {
	forbidden := fmt.Errorf("%w: Git cache contains an indirect metadata path", domain.ErrForbidden)
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == filepath.Join(dir, "commondir") || path == filepath.Join(dir, "objects", "info", "alternates") {
			return forbidden
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return forbidden
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Nlink != 1 {
			return forbidden
		}
		return nil
	})
}

func (g gitRunner) validFetch(args []string) bool {
	if len(args) < 6 || !slices.Equal(args[:3], []string{"--no-tags", "--prune", "--no-auto-maintenance"}) {
		return false
	}
	args = args[3:]
	if raw, ok := strings.CutPrefix(args[0], "--depth="); ok {
		depth, err := strconv.Atoi(raw)
		if err != nil || depth < 1 {
			return false
		}
		args = args[1:]
	}
	if len(args) != 3 || args[0] != "--" || !g.remoteURL(args[1]) {
		return false
	}
	ref, err := NormalizeRef(args[2])
	return err == nil && args[2] == ref
}

const commitFormat = "--format=%H%x00%T%x00%P%x00%an%x00%ae%x00%aI%x00%cn%x00%ce%x00%cI%x00%s"
const refFormat = "--format=%(refname)%00%(objectname)%00%(*objectname)%00%(objecttype)"

func validRepoPath(path string) bool {
	if path == "" || !utf8.ValidString(path) || path[0] == '-' ||
		strings.ContainsRune(path, '\\') || strings.ContainsFunc(path, unicode.IsControl) {
		return false
	}
	for part := range strings.SplitSeq(path, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}

func validGitNumber(arg, prefix string, minimum int) bool {
	raw, ok := strings.CutPrefix(arg, prefix)
	if !ok || raw == "" || strings.ContainsFunc(raw, func(r rune) bool { return r < '0' || r > '9' }) {
		return false
	}
	n, err := strconv.Atoi(raw)
	return err == nil && n >= minimum
}

func readOnlyCommand(command string, args []string) bool {
	switch command {
	case "rev-parse":
		if slices.Equal(args, []string{"--is-shallow-repository"}) {
			return true
		}
		if len(args) != 3 || args[0] != "--verify" || args[1] != "--end-of-options" {
			return false
		}
		ref, ok := strings.CutSuffix(args[2], "^{commit}")
		normalized, err := NormalizeRef(ref)
		return ok && err == nil && ref == normalized
	case "cat-file":
		return slices.Equal(args, []string{"--batch"}) ||
			len(args) == 2 && args[0] == "commit" && validOID(args[1])
	case "show":
		if len(args) == 0 || !validOID(args[len(args)-1]) {
			return false
		}
		return slices.Equal(args[:len(args)-1], []string{"--no-patch", "--no-notes", "--no-show-signature", "--format=%B"}) ||
			len(args) == 6 && slices.Equal(args[:4], []string{"-s", "--no-show-signature", "--no-notes", "--encoding=UTF-8"}) &&
				args[4] == commitFormat
	case "ls-tree":
		return len(args) == 4 && slices.Equal(args[:3], []string{"-r", "-l", "-z"}) && validOID(args[3]) ||
			len(args) == 5 && slices.Equal(args[:2], []string{"-l", "-z"}) && fileRevision(args[2:])
	case "for-each-ref":
		return slices.Equal(args, []string{refFormat, "refs/heads", "refs/tags"})
	case "symbolic-ref":
		return slices.Equal(args, []string{"--quiet", "HEAD"})
	case "log":
		if len(args) < 13 || !slices.Equal(args[:3], []string{"--no-notes", "--no-show-signature", "--encoding=UTF-8"}) {
			return false
		}
		args = args[3:]
		if args[0] == "--follow" {
			args = args[1:]
		}
		return len(args) == 10 && slices.Equal(args[:4], []string{"-M", "-l256", "--name-status", "-z"}) &&
			validGitNumber(args[4], "--skip=", 0) && validGitNumber(args[5], "--max-count=", 1) &&
			args[6] == commitFormat && fileRevision(args[7:])
	case "blame":
		if len(args) < 5 || !slices.Equal(args[:2], []string{"--no-textconv", "--porcelain"}) {
			return false
		}
		args = args[2:]
		if len(args) == 5 && args[0] == "-L" {
			start, end, ok := strings.Cut(args[1], ",")
			if !ok || !validGitNumber(start, "", 1) || !validGitNumber(end, "", 1) {
				return false
			}
			first, _ := strconv.Atoi(start)
			last, _ := strconv.Atoi(end)
			return first <= last && fileRevision(args[2:])
		}
		return fileRevision(args)
	case "rev-list":
		if len(args) != 4 || args[0] != "--topo-order" || !validOID(args[2]) || args[3] != "--" {
			return false
		}
		count, ok := strings.CutPrefix(args[1], "--max-count=")
		n, err := strconv.Atoi(count)
		return ok && err == nil && n > 0
	case "diff-tree":
		flags := []string{"--no-commit-id", "--no-ext-diff", "--no-textconv", "-r", "-M", "-l256", "-z"}
		if len(args) != len(flags)+4 || !slices.Equal(args[:len(flags)], flags) {
			return false
		}
		args = args[len(flags):]
		return (args[0] == "--name-status" || args[0] == "--numstat") &&
			(args[1] == "--root" || validOID(args[1])) && validOID(args[2]) && args[3] == "--"
	case "diff":
		flags := []string{"--no-color", "--no-ext-diff", "--no-textconv", "--submodule=short", "-O/dev/null", "-M", "-l256"}
		return len(args) == len(flags)+3 && slices.Equal(args[:len(flags)], flags) &&
			validOID(args[len(flags)]) && validOID(args[len(flags)+1]) && args[len(flags)+2] == "--"
	}
	return false
}

func fileRevision(args []string) bool {
	return len(args) == 3 && validOID(args[0]) && args[1] == "--" && validRepoPath(args[2])
}

type boundedOutput struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := b.limit - b.buffer.Len(); n > remaining {
		b.truncated = true
		p = p[:remaining]
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}

type gitError struct {
	err        error
	diagnostic string
}

func (e *gitError) Error() string { return fmt.Sprintf("git command failed: %v", e.err) }
func (e *gitError) Unwrap() error { return e.err }

var errOutputLimit = errors.New("Git output exceeded the scanner limit")

func (g gitRunner) run(ctx context.Context, limit int, auth bool, args ...string) ([]byte, error) {
	ctx, span := telemetry.Span(ctx, "git.command")
	defer span.End()
	cmd := g.command(ctx, auth, args...)
	if cmd.Err != nil {
		return nil, cmd.Err
	}
	stdout := &boundedOutput{limit: limit}
	stderr := &boundedOutput{limit: 8192}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		// Stderr may echo request headers or repository-controlled values. Keep
		// it private and use it only for classifying Git failures.
		diagnostic := stderr.buffer.String()
		if g.token != "" {
			diagnostic = strings.ReplaceAll(diagnostic, g.token, "[redacted]")
		}
		return nil, &gitError{err: err, diagnostic: diagnostic}
	}
	if stdout.truncated {
		return stdout.buffer.Bytes(), errOutputLimit
	}
	return stdout.buffer.Bytes(), nil
}

func (g gitRunner) resolve(ctx context.Context, dir, ref string) (string, error) {
	out, err := g.run(ctx, 1024, false, "--git-dir="+dir, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	oid := strings.TrimSpace(string(out))
	if !validOID(oid) {
		return "", errors.New("Git returned an invalid commit ID")
	}
	return oid, nil
}

func (g gitRunner) isShallow(ctx context.Context, dir string) (bool, error) {
	out, err := g.run(ctx, 64, false, "--git-dir="+dir, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return false, err
	}
	switch strings.TrimSpace(string(out)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, errors.New("Git returned an invalid shallow repository status")
	}
}
