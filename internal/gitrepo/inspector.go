package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"projectlens/internal/domain"
)

type Metadata struct {
	Name              string
	CanonicalSource   string
	DefaultBranch     string
	RemoteURL         string
	GitDir, CommonDir string
}

type LocalInspector struct {
	roots []string
}

func NewLocalInspector(allowedRoots []string) *LocalInspector {
	return &LocalInspector{roots: append([]string(nil), allowedRoots...)}
}

type localRoot struct {
	configured string
	resolved   string
}

func inside(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	return rel, err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func pathError(err error) error {
	switch {
	case errors.Is(err, os.ErrPermission):
		return fmt.Errorf("%w: local repository path is unreadable", domain.ErrForbidden)
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%w: local repository path does not exist: %w", domain.ErrInvalidInput, os.ErrNotExist)
	default:
		return fmt.Errorf("%w: cannot inspect local repository path", domain.ErrInvalidInput)
	}
}

func outsideRoots() error {
	return fmt.Errorf("%w: local repository path is outside configured roots", domain.ErrForbidden)
}

// Check link destinations before following them, including dangling links.
func resolveWithin(path string, roots []localRoot) (string, error) {
	const separator = string(filepath.Separator)
	for links := 0; links <= 255; links++ {
		var base, relative string
		for _, root := range roots {
			for _, prefix := range []string{root.configured, root.resolved} {
				prefix = strings.TrimSuffix(prefix, separator)
				if rest, ok := strings.CutPrefix(path, prefix+separator); ok {
					base, relative = root.resolved, rest
					break
				}
				if path == prefix {
					base = root.resolved
					break
				}
			}
			if base != "" {
				break
			}
		}
		if base == "" {
			return "", outsideRoots()
		}
		boundary := base
		parts := strings.Split(relative, separator)
		followed := false
		for idx, part := range parts {
			switch part {
			case "", ".":
				continue
			case "..":
				if base == boundary {
					return "", outsideRoots()
				}
				base = filepath.Dir(base)
				continue
			}
			base = filepath.Join(base, part)
			info, err := os.Lstat(base)
			if err != nil {
				return "", pathError(err)
			}
			if info.Mode()&os.ModeSymlink == 0 {
				continue
			}
			target, err := os.Readlink(base)
			if err != nil {
				return "", pathError(err)
			}
			path = metadataPath(filepath.Dir(base), target)
			if idx+1 < len(parts) {
				path += separator + strings.Join(parts[idx+1:], separator)
			}
			followed = true
			break
		}
		if !followed {
			return base, nil
		}
	}
	return "", fmt.Errorf("%w: too many symlinks", domain.ErrInvalidInput)
}

func metadataPath(base, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	// Preserve the order of symlinks and '..' components.
	return base + string(filepath.Separator) + path
}

func readMetadata(path string) (string, error) {
	const limit = 1 << 20
	info, err := os.Stat(path)
	if err != nil {
		return "", pathError(err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: expected a regular Git metadata file", domain.ErrInvalidInput)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", pathError(err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return "", pathError(err)
	}
	if len(data) > limit || bytes.ContainsRune(data, 0) {
		return "", fmt.Errorf("%w: invalid Git metadata file", domain.ErrInvalidInput)
	}
	return string(data), nil
}

func requiredDirectory(path string, roots []localRoot) (string, error) {
	dir, err := resolveWithin(path, roots)
	if err != nil {
		return "", err
	}
	// Check the lexical form too. Git can normalize a path before opening it.
	if clean := filepath.Clean(path); clean != path {
		normalized, err := resolveWithin(clean, roots)
		if err != nil {
			return "", err
		}
		if normalized != dir {
			return "", fmt.Errorf("%w: ambiguous Git metadata directory", domain.ErrForbidden)
		}
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", pathError(err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: expected a Git directory", domain.ErrInvalidInput)
	}
	for _, root := range roots {
		if relative, ok := inside(root.resolved, dir); ok && relative != "." {
			return dir, nil
		}
	}
	return "", outsideRoots()
}

func validateGitMetadata(gitDir string, roots []localRoot) (string, error) {
	commonDir := gitDir
	pointer, err := resolveWithin(filepath.Join(gitDir, "commondir"), roots)
	if err == nil {
		data, err := readMetadata(pointer)
		if err != nil {
			return "", err
		}
		commonDir, err = requiredDirectory(metadataPath(gitDir, strings.TrimRight(data, "\r\n")), roots)
		if err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	seenObjects := make(map[string]bool)
	for _, dir := range []string{gitDir, commonDir} {
		for _, name := range []string{"HEAD", "config", "commondir", "shallow"} {
			path, err := resolveWithin(filepath.Join(dir, name), roots)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return "", err
			}
			info, err := os.Stat(path)
			if err != nil {
				return "", pathError(err)
			}
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("%w: expected a regular Git metadata file", domain.ErrInvalidInput)
			}
		}
		objects, err := requiredDirectory(filepath.Join(dir, "objects"), roots)
		if errors.Is(err, os.ErrNotExist) && dir != commonDir {
			continue
		}
		if err != nil {
			return "", err
		}
		if err := validateAlternates(filepath.Join(dir, "objects"), objects, roots, seenObjects, 0); err != nil {
			return "", err
		}
	}
	return commonDir, nil
}

func validateAlternates(path, resolved string, roots []localRoot, seen map[string]bool, depth int) error {
	if seen[path] {
		return nil
	}
	if depth > 64 {
		return fmt.Errorf("%w: too many levels of alternate Git object stores", domain.ErrInvalidInput)
	}
	seen[path] = true
	file, err := resolveWithin(filepath.Join(resolved, "info", "alternates"), roots)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	data, err := readMetadata(file)
	if err != nil {
		return err
	}
	for entry := range strings.SplitSeq(data, "\n") {
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}
		if strings.HasPrefix(entry, "\"") {
			entry, err = strconv.Unquote(entry)
			if err != nil {
				return fmt.Errorf("%w: invalid quoted Git alternate", domain.ErrInvalidInput)
			}
		}
		alternate := metadataPath(path, entry)
		dir, err := requiredDirectory(alternate, roots)
		if err != nil {
			return err
		}
		if err := validateAlternates(filepath.Clean(alternate), dir, roots, seen, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (i *LocalInspector) Inspect(ctx context.Context, rawPath string) (Metadata, error) {
	if len(i.roots) == 0 {
		return Metadata{}, outsideRoots()
	}
	path := strings.TrimSpace(rawPath)
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return Metadata{}, fmt.Errorf("%w: source must be an absolute local path without NUL", domain.ErrInvalidInput)
	}
	var roots []localRoot
	for _, rawRoot := range i.roots {
		root := filepath.Clean(rawRoot)
		if !filepath.IsAbs(root) {
			continue
		}
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			if _, ok := inside(root, path); ok && errors.Is(err, os.ErrPermission) {
				return Metadata{}, pathError(err)
			}
			continue
		}
		roots = append(roots, localRoot{configured: root, resolved: resolved})
	}
	canonical, err := resolveWithin(path, roots)
	if err != nil {
		return Metadata{}, err
	}
	canonical, err = filepath.EvalSymlinks(canonical)
	if err != nil {
		return Metadata{}, pathError(err)
	}
	allowed := false
	for _, root := range roots {
		if _, ok := inside(root.resolved, canonical); ok {
			allowed = true
			break
		}
	}
	if !allowed {
		return Metadata{}, outsideRoots()
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return Metadata{}, pathError(err)
	}
	if !info.IsDir() {
		return Metadata{}, fmt.Errorf("%w: source is not a directory", domain.ErrInvalidInput)
	}
	if err := checkReadable(canonical); err != nil {
		return Metadata{}, err
	}
	gitPath, err := resolveWithin(filepath.Join(canonical, ".git"), roots)
	if errors.Is(err, os.ErrNotExist) {
		return Metadata{}, fmt.Errorf("%w: directory is not a Git repository", domain.ErrInvalidInput)
	}
	if err != nil {
		return Metadata{}, err
	}
	if err := checkReadable(gitPath); err != nil {
		return Metadata{}, err
	}
	gitDir := gitPath
	info, err = os.Stat(gitPath)
	if err != nil {
		return Metadata{}, pathError(err)
	}
	if info.Mode().IsRegular() {
		pointer, err := readMetadata(gitPath)
		if err != nil {
			return Metadata{}, err
		}
		target, ok := strings.CutPrefix(strings.TrimRight(pointer, "\r\n"), "gitdir: ")
		if !ok || target == "" {
			return Metadata{}, fmt.Errorf("%w: invalid .git pointer", domain.ErrInvalidInput)
		}
		// Git interprets relative pointers from the work tree's .git entry.
		gitDir, err = requiredDirectory(metadataPath(canonical, target), roots)
		if err != nil {
			return Metadata{}, err
		}
	}
	commonDir, err := validateGitMetadata(gitDir, roots)
	if err != nil {
		return Metadata{}, err
	}
	for _, query := range []struct{ option, expected string }{
		{"--git-dir", gitDir}, {"--git-common-dir", commonDir},
	} {
		raw, err := runGit(ctx, canonical, "rev-parse", "--path-format=absolute", query.option)
		if err != nil {
			return Metadata{}, err
		}
		if !filepath.IsAbs(raw) {
			return Metadata{}, fmt.Errorf("%w: Git returned a relative repository directory", domain.ErrInvalidInput)
		}
		resolved, err := requiredDirectory(raw, roots)
		if err != nil {
			return Metadata{}, err
		}
		if resolved != query.expected {
			return Metadata{}, fmt.Errorf("%w: Git reported an unexpected repository directory", domain.ErrForbidden)
		}
	}

	output, err := runGit(ctx, canonical, "rev-parse", "--is-inside-work-tree", "--show-toplevel")
	if err != nil {
		return Metadata{}, err
	}
	insideTree, top, _ := strings.Cut(output, "\n")
	if insideTree != "true" {
		return Metadata{}, fmt.Errorf("%w: source is not a Git work tree", domain.ErrInvalidInput)
	}
	if !filepath.IsAbs(top) {
		return Metadata{}, fmt.Errorf("%w: Git did not return an absolute work tree path", domain.ErrInvalidInput)
	}
	top, err = resolveWithin(top, roots)
	if err != nil {
		return Metadata{}, err
	}
	if top != canonical {
		return Metadata{}, fmt.Errorf("%w: source must be the root of the Git work tree", domain.ErrInvalidInput)
	}

	branch, err := runGit(ctx, canonical, "symbolic-ref", "--short", "HEAD")
	if errors.Is(err, domain.ErrInvalidInput) {
		branch, err = runGit(ctx, canonical, "rev-parse", "--abbrev-ref", "HEAD")
	}
	if err != nil && !errors.Is(err, domain.ErrInvalidInput) {
		return Metadata{}, err
	}
	remote, err := runGit(ctx, canonical, "config", "--local", "--no-includes", "--get", "remote.origin.url")
	if err != nil && !errors.Is(err, domain.ErrInvalidInput) {
		return Metadata{}, err
	}
	if target, err := url.Parse(remote); err == nil && (target.Scheme == "http" || target.Scheme == "https") && target.User != nil {
		target.User = nil
		remote = target.String()
	}
	return Metadata{Name: filepath.Base(canonical), CanonicalSource: canonical, DefaultBranch: branch, RemoteURL: remote, GitDir: gitDir, CommonDir: commonDir}, nil
}

func checkReadable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return pathError(err)
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return fmt.Errorf("%w: expected a directory or Git file", domain.ErrInvalidInput)
	}
	file, err := os.Open(path)
	if err != nil {
		return pathError(err)
	}
	defer file.Close()
	if info.IsDir() {
		_, err = file.Readdirnames(1)
	} else {
		var sample [1]byte
		_, err = file.Read(sample[:])
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return pathError(err)
	}
	return nil
}

// GitCommand applies the shared process and environment restrictions. Callers
// authorize the subcommand and arguments before constructing the command.
func GitCommand(ctx context.Context, protocol string, args ...string) *exec.Cmd {
	flags := []string{
		"--no-replace-objects",
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.symlinks=false",
		"-c", "core.fsmonitor=false",
		"-c", "core.pager=cat",
		"-c", "core.editor=false",
		"-c", "core.sshCommand=false",
		"-c", "core.askPass=",
		"-c", "core.alternateRefsCommand=false",
		"-c", "core.attributesFile=/dev/null",
		"-c", "submodule.recurse=false",
		"-c", "credential.helper=",
		"-c", "protocol.allow=never",
		"-c", "protocol.file.allow=never",
		"-c", "gc.auto=0",
		"-c", "maintenance.auto=false",
		"-c", "fetch.writeCommitGraph=false",
		"-c", "diff.external=",
		"-c", "diff.orderFile=/dev/null",
		"-c", "diff.textconv=",
		"-c", "blame.ignoreRevsFile=",
		"-c", "log.mailmap=false",
		"-c", "log.follow=false",
		"-c", "log.showRoot=true",
		"-c", "mailmap.file=/dev/null",
		"-c", "mailmap.blob=",
		"-c", "push.default=nothing",
	}
	cmd := exec.CommandContext(ctx, "git", append(flags, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "GIT_") {
			continue
		}
		switch name {
		case "LC_ALL", "PAGER", "EDITOR", "VISUAL":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "LC_ALL=C", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_OPTIONAL_LOCKS=0", "GIT_SSH_COMMAND=false", "GIT_ASKPASS=",
		"GIT_PAGER=cat", "GIT_EDITOR=false", "GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL="+protocol,
		"GIT_ATTR_NOSYSTEM=1")
	return cmd
}

type inspectionOutput struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (out *inspectionOutput) Write(p []byte) (int, error) {
	n := len(p)
	if n > out.limit-out.buffer.Len() {
		out.truncated = true
		p = p[:out.limit-out.buffer.Len()]
	}
	_, _ = out.buffer.Write(p)
	return n, nil
}

func runGit(ctx context.Context, path string, args ...string) (string, error) {
	if len(args) == 0 || (args[0] != "rev-parse" && args[0] != "symbolic-ref" &&
		!slices.Equal(args, []string{"config", "--local", "--no-includes", "--get", "remote.origin.url"})) ||
		(args[0] == "symbolic-ref" && !slices.Equal(args, []string{"symbolic-ref", "--short", "HEAD"})) {
		return "", fmt.Errorf("%w: Git command is outside the inspection capability", domain.ErrForbidden)
	}
	cmd := GitCommand(ctx, "", append([]string{"-C", path}, args...)...)
	cmd.Env = append(cmd.Env, "GIT_CEILING_DIRECTORIES="+filepath.Dir(path))
	stdout := &inspectionOutput{limit: 64 << 10}
	stderr := &inspectionOutput{limit: 8192}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err == nil {
		if stdout.truncated {
			return "", fmt.Errorf("%w: local Git output exceeds the inspection limit", domain.ErrInvalidInput)
		}
		return strings.TrimSuffix(stdout.buffer.String(), "\n"), nil
	}
	if errors.Is(err, os.ErrPermission) || strings.Contains(stderr.buffer.String(), "Permission denied") || strings.Contains(stderr.buffer.String(), "Operation not permitted") {
		return "", fmt.Errorf("%w: Git cannot read the local repository", domain.ErrForbidden)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return "", fmt.Errorf("run local Git: %w", err)
	}
	return "", fmt.Errorf("%w: Git could not inspect the work tree", domain.ErrInvalidInput)
}

func LogLocalRootMounts(log *slog.Logger, roots []string) {
	for _, root := range roots {
		readOnly, known, err := InspectRootReadOnly(root)
		if known && readOnly && err == nil {
			log.Info("local repository root has an OS-enforced read-only mount", "root", root, "os_readonly_mount", true)
			continue
		}
		log.Warn("local repository root has no confirmed OS read-only mount. Native deployments without an OS read-only mount enforce read-only access at the application level and lack OS-enforced mandatory read-only isolation. Use Docker :ro bind mounts or an OS-level read-only mount or restricted user.",
			"root", root, "os_readonly_mount", false, "os_readonly_mount_known", known, "error", err)
	}
}
