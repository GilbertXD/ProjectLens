package gitscan

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"projectlens/internal/domain"
)

const HistoryPage = 100
const HistoryLimit = 2000
const BlameLineLimit = 2000
const refOutputLimit = 8 << 20

type Change struct {
	Path       string `json:"path"`
	OldPath    string `json:"old_path,omitempty"`
	Type       string `json:"type"`
	Binary     bool   `json:"binary"`
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
}
type Changes struct {
	Patch             string   `json:"patch,omitempty"`
	PatchTruncated    bool     `json:"patch_truncated,omitempty"`
	Files             []Change `json:"files"`
	Truncated         bool     `json:"truncated"`
	ParentUnavailable bool     `json:"parent_unavailable,omitempty"`
}
type HistoryCommit struct {
	CommitInfo
	Message   string `json:"message"`
	URL       string `json:"html_url,omitempty"`
	Truncated bool   `json:"truncated"`
}
type HistoryPageResult struct {
	Tip            string   `json:"tip"`
	Reachable      []string `json:"-"`
	HasMore        bool     `json:"has_more"`
	Imported       int      `json:"imported"`
	ReachableCount int      `json:"reachable_count"`
	HistoryWindow
}

type HistoryWindow struct {
	WindowLimit        int    `json:"window_limit"`
	PageSize           int    `json:"page_size"`
	Complete           bool   `json:"complete"`
	Truncated          bool   `json:"truncated"`
	WindowLimitReached bool   `json:"window_limit_reached"`
	WindowStatus       string `json:"window_status"`
}

func DescribeHistoryWindow(reachable int, hasMore bool) HistoryWindow {
	window := HistoryWindow{
		WindowLimit: HistoryLimit, PageSize: HistoryPage, Complete: !hasMore, Truncated: hasMore,
		WindowLimitReached: hasMore && reachable >= HistoryLimit,
	}
	switch {
	case window.Complete:
		window.WindowStatus = "complete"
	case window.WindowLimitReached:
		window.WindowStatus = "window_limit_reached"
	default:
		window.WindowStatus = "partial"
	}
	return window
}

func (s *Scanner) History(ctx context.Context, spec SourceSpec, limit int, missing func([]string) (map[string]bool, error), save func(HistoryCommit, Changes) error) (result HistoryPageResult, err error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.GitTimeout)
	defer cancel()
	limit = min(max(limit, 1), HistoryLimit)
	spec.depth = limit + 2
	repo, err := s.prepare(ctx, spec)
	if err != nil {
		return result, err
	}
	defer repo.Release()
	git := gitRunner{}
	dir := "--git-dir=" + repo.GitDir
	raw, err := git.run(ctx, (limit+1)*66, false, dir, "rev-list", "--topo-order", "--max-count="+strconv.Itoa(limit+1), repo.PinnedCommit, "--")
	if err != nil {
		return result, err
	}
	ids := strings.Fields(string(raw))
	result.HasMore = len(ids) > limit
	ids = ids[:min(limit, len(ids))]
	boundaries, err := shallowBoundaries(repo, ids)
	if err != nil {
		return result, err
	}
	result.HasMore = result.HasMore || len(boundaries) > 0
	result.Tip = repo.PinnedCommit
	result.Reachable = ids
	result.ReachableCount = len(ids)
	result.HistoryWindow = DescribeHistoryWindow(result.ReachableCount, result.HasMore)
	wanted, err := missing(ids)
	if err != nil {
		return result, err
	}
	for _, sha := range ids {
		if !wanted[sha] {
			continue
		}
		copy := *repo
		copy.PinnedCommit = sha
		info, err := readCommit(ctx, &copy)
		if err != nil {
			return result, err
		}
		msg, err := git.run(ctx, 65536, false, dir, "show", "--no-patch", "--no-notes", "--no-show-signature", "--format=%B", sha)
		truncated := errors.Is(err, errOutputLimit)
		if err != nil && !truncated {
			return result, err
		}
		item := HistoryCommit{CommitInfo: info, Message: strings.ToValidUTF8(strings.ReplaceAll(string(msg), "\x00", ""), "�"), Truncated: truncated}
		if spec.SourceType == domain.RepositorySourceGitHub {
			item.URL = repo.CanonicalSource + "/commit/" + sha
		}
		var base string
		if len(info.Parents) > 0 {
			base = info.Parents[0]
		}
		var changes Changes
		if base != "" && boundaries[sha] {
			// A shallow boundary still records its parents, but Git may
			// lack the first parent needed to calculate the changes.
			_, err := git.resolve(ctx, repo.GitDir, base)
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				changes = Changes{Files: []Change{}, Truncated: true, ParentUnavailable: true}
			} else if err != nil {
				return result, err
			}
		}
		if !changes.ParentUnavailable {
			changes, err = readChanges(ctx, repo.GitDir, base, sha)
			if err != nil {
				return result, err
			}
		}
		if err := save(item, changes); err != nil {
			return result, err
		}
		result.Imported++
	}
	return result, nil
}

func shallowBoundaries(repo *PreparedRepo, ids []string) (map[string]bool, error) {
	if !repo.Shallow {
		return nil, nil
	}
	dir := repo.CommonDir
	if dir == "" {
		dir = repo.GitDir
	}
	file, err := os.Open(filepath.Join(dir, "shallow"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	reachable := make(map[string]bool, len(ids))
	for _, id := range ids {
		reachable[id] = true
	}
	boundaries := make(map[string]bool)
	lines := bufio.NewScanner(file)
	for lines.Scan() {
		if id := lines.Text(); reachable[id] {
			boundaries[id] = true
		}
	}
	return boundaries, lines.Err()
}

// Git's -z formats preserve tabs and newlines in filenames. The two commands
// use identical rename settings and order. Binary counts are a pair of dashes.
func readChanges(ctx context.Context, dir, base, head string) (Changes, error) {
	out := Changes{Files: []Change{}}
	args := []string{"--git-dir=" + dir, "diff-tree", "--no-commit-id", "--no-ext-diff", "--no-textconv", "-r", "-M", "-l256", "-z"}
	revisions := []string{"--root", head, "--"}
	if base != "" {
		revisions = []string{base, head, "--"}
	}
	read := func(format string) ([]string, error) {
		cmd := append(append([]string{}, args...), format)
		raw, err := (gitRunner{}).run(ctx, 256<<10, false, append(cmd, revisions...)...)
		if errors.Is(err, errOutputLimit) {
			out.Truncated = true
			err = nil
		}
		fields := strings.Split(string(raw), "\x00")
		return fields[:len(fields)-1], err
	}
	names, err := read("--name-status")
	if err != nil {
		return out, err
	}
	stats, err := read("--numstat")
	if err != nil {
		return out, err
	}
	for len(names) >= 2 && len(stats) > 0 && len(out.Files) < 500 {
		status, path := names[0], names[1]
		names = names[2:]
		parts := strings.SplitN(stats[0], "\t", 3)
		stats = stats[1:]
		if status == "" || len(parts) != 3 {
			return out, errors.New("invalid Git change record")
		}
		ch := Change{Type: status[:1], Path: path, Binary: parts[0] == "-"}
		if !ch.Binary {
			ch.Insertions, err = strconv.Atoi(parts[0])
			if err != nil {
				return out, err
			}
			ch.Deletions, err = strconv.Atoi(parts[1])
			if err != nil {
				return out, err
			}
		}
		if ch.Type == "R" || ch.Type == "C" {
			if len(names) < 1 || len(stats) < 2 {
				out.Truncated = true
				break
			}
			ch.OldPath, ch.Path = ch.Path, names[0]
			names = names[1:]
			stats = stats[2:]
		}
		out.Files = append(out.Files, ch)
	}
	out.Truncated = out.Truncated || len(names) > 0 || len(stats) > 0
	return out, nil
}

func ValidSHA(sha string) bool { return validOID(sha) }
func (s *Scanner) Compare(ctx context.Context, spec SourceSpec, base, head string) (Changes, error) {
	if !validOID(base) || !validOID(head) {
		return Changes{}, fmt.Errorf("%w: expected full commit SHAs", domain.ErrInvalidInput)
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.GitTimeout)
	defer cancel()
	spec.Ref = head
	spec.compareBase = base
	repo, err := s.prepare(ctx, spec)
	if err != nil {
		return Changes{}, err
	}
	defer repo.Release()
	changes, err := readChanges(ctx, repo.GitDir, base, repo.PinnedCommit)
	if err != nil {
		return changes, err
	}
	raw, err := (gitRunner{}).run(ctx, 64<<10, false, "--git-dir="+repo.GitDir, "diff", "--no-color", "--no-ext-diff", "--no-textconv", "--submodule=short", "-O/dev/null", "-M", "-l256", base, repo.PinnedCommit, "--")
	changes.PatchTruncated = errors.Is(err, errOutputLimit)
	if err != nil && !changes.PatchTruncated {
		return changes, err
	}
	if bytes.ContainsRune(raw, 0) {
		changes.PatchTruncated = true
	} else {
		changes.Patch = strings.ToValidUTF8(string(raw), "?")
	}
	changes.Truncated = changes.Truncated || changes.PatchTruncated
	return changes, nil
}

type CommitTreeEntry struct {
	Path         string `json:"path"`
	BlobOID      string `json:"blob_oid"`
	Mode         string `json:"mode"`
	Size         int64  `json:"size"`
	IsExecutable bool   `json:"is_executable"`
}

type CommitTreeResult struct {
	CommitHash string            `json:"commit_hash"`
	TreeHash   string            `json:"tree_hash"`
	PathPrefix string            `json:"path_prefix"`
	Total      int               `json:"total"`
	Limit      int               `json:"limit"`
	Offset     int               `json:"offset"`
	Items      []CommitTreeEntry `json:"items"`
}

type CommitFileResult struct {
	CommitTreeEntry
	CommitHash string `json:"commit_hash"`
	SHA256     string `json:"sha256"`
	Binary     bool   `json:"binary"`
	LineCount  int    `json:"line_count"`
	Content    string `json:"content,omitempty"`
}

func validHistoryPage(limit, offset int) bool {
	return limit >= 1 && limit <= HistoryPage && offset >= 0 && offset <= math.MaxInt32
}

func (s *Scanner) TreeAtCommit(ctx context.Context, spec SourceSpec, sha, pathPrefix string, limit, offset int) (CommitTreeResult, error) {
	var result CommitTreeResult
	// Normalize a directory suffix while continuing to reject empty path segments.
	if pathPrefix != "" {
		pathPrefix = strings.TrimSuffix(pathPrefix, "/")
		if !validRepoPath(pathPrefix) {
			return result, fmt.Errorf("%w: invalid repository path prefix", domain.ErrInvalidInput)
		}
	}
	if !validOID(sha) || !validHistoryPage(limit, offset) {
		return result, fmt.Errorf("%w: expected a full commit SHA and valid pagination", domain.ErrInvalidInput)
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.GitTimeout)
	defer cancel()
	spec.Ref, spec.requireCommit, spec.preferCache = sha, true, true
	repo, err := s.prepare(ctx, spec)
	if err != nil {
		return result, err
	}
	defer repo.Release()
	commit, err := readCommit(ctx, repo)
	if err != nil {
		return result, err
	}
	entries, err := readTree(ctx, repo)
	if err != nil {
		return result, historicalTreeError(err)
	}
	result = CommitTreeResult{
		CommitHash: commit.Hash, TreeHash: commit.TreeHash, PathPrefix: pathPrefix,
		Limit: limit, Offset: offset, Items: []CommitTreeEntry{},
	}
	for _, entry := range entries {
		if entry.kind != "blob" || !regularFileMode(entry.Mode) || !validRepoPath(entry.Path) {
			continue
		}
		if pathPrefix != "" && entry.Path != pathPrefix && !strings.HasPrefix(entry.Path, pathPrefix+"/") {
			continue
		}
		if result.Total >= offset && len(result.Items) < limit {
			result.Items = append(result.Items, CommitTreeEntry{
				Path: entry.Path, BlobOID: entry.BlobOID, Mode: entry.Mode, Size: entry.Size, IsExecutable: entry.Mode == "100755",
			})
		}
		result.Total++
	}
	return result, nil
}

func regularFileMode(mode string) bool { return mode == "100644" || mode == "100755" }

func historicalTreeError(err error) error {
	detail := gitDiagnostic(err)
	if strings.Contains(detail, "not a tree object") || strings.Contains(detail, "unable to read tree") ||
		strings.Contains(detail, "bad tree object") {
		return fmt.Errorf("%w: tree object is unavailable", domain.ErrNotFound)
	}
	return err
}

func lookupCommitPath(ctx context.Context, repo *PreparedRepo, path string) (treeEntry, error) {
	var entry treeEntry
	raw, err := (gitRunner{}).run(ctx, treeOutputLimit, false, "--git-dir="+repo.GitDir, "ls-tree", "-l", "-z", repo.PinnedCommit, "--", path)
	if err != nil {
		return entry, historicalTreeError(err)
	}
	for record := range bytes.SplitSeq(raw, []byte{0}) {
		header, name, ok := bytes.Cut(record, []byte{'\t'})
		if !ok || string(name) != path {
			continue
		}
		fields := strings.Fields(string(header))
		if len(fields) != 4 || !validOID(fields[2]) {
			return entry, errors.New("invalid Git tree entry")
		}
		entry = treeEntry{kind: fields[1], SkippedFile: SkippedFile{Path: path, Mode: fields[0], BlobOID: fields[2], Size: -1}}
		if fields[3] != "-" && fields[3] != "BAD" {
			entry.Size, err = strconv.ParseInt(fields[3], 10, 64)
			if err != nil || entry.Size < 0 {
				return treeEntry{}, errors.New("invalid Git tree object size")
			}
		}
		return entry, nil
	}
	return entry, fmt.Errorf("%w: path not found at commit", domain.ErrNotFound)
}

func (s *Scanner) readHistoricalFile(ctx context.Context, repo *PreparedRepo, filePath string) (CommitFileResult, error) {
	var result CommitFileResult
	entry, err := lookupCommitPath(ctx, repo, filePath)
	if err != nil {
		return result, err
	}
	if entry.kind == "tree" {
		return result, fmt.Errorf("%w: path is a directory", domain.ErrNotFound)
	}
	if entry.kind != "blob" || !regularFileMode(entry.Mode) {
		return result, fmt.Errorf("%w: path must be a regular file", domain.ErrInvalidInput)
	}
	if entry.Size < 0 {
		return result, fmt.Errorf("%w: blob is unavailable", domain.ErrNotFound)
	}
	if entry.Size > s.cfg.MaxFileSize {
		return result, fmt.Errorf("%w: blob exceeds maximum file size", domain.ErrPayloadTooLarge)
	}
	batch, err := startBlobBatch(ctx, repo.GitDir)
	if err != nil {
		return result, err
	}
	defer batch.abort()
	content, err := batch.read(entry.BlobOID, entry.Size, s.cfg.MaxFileSize)
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if errors.Is(err, errBlobUnavailable) {
		return result, fmt.Errorf("%w: blob is unavailable", domain.ErrNotFound)
	}
	if err != nil {
		return result, err
	}
	if err := batch.finish(ctx); err != nil {
		return result, err
	}
	file := describeFile(entry, content, ScanOptions{})
	result = CommitFileResult{
		CommitHash: repo.PinnedCommit, SHA256: file.SHA256, Binary: isBinary(content),
		CommitTreeEntry: CommitTreeEntry{Path: file.Path, BlobOID: file.BlobOID, Mode: file.Mode, Size: file.Size, IsExecutable: file.IsExecutable},
	}
	if !result.Binary {
		result.LineCount = file.LineCount
		result.Content = string(content)
	}
	return result, nil
}

func (s *Scanner) FileAtCommit(ctx context.Context, spec SourceSpec, sha, filePath string) (CommitFileResult, error) {
	if !validOID(sha) || !validRepoPath(filePath) {
		return CommitFileResult{}, fmt.Errorf("%w: expected a full commit SHA and valid repository path", domain.ErrInvalidInput)
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.GitTimeout)
	defer cancel()
	spec.Ref, spec.requireCommit, spec.preferCache = sha, true, true
	repo, err := s.prepare(ctx, spec)
	if err != nil {
		return CommitFileResult{}, err
	}
	defer repo.Release()
	return s.readHistoricalFile(ctx, repo, filePath)
}

type FileHistoryEntry struct {
	CommitInfo
	URL        string `json:"html_url,omitempty"`
	ChangeType string `json:"change_type"`
	Path       string `json:"path"`
	OldPath    string `json:"old_path,omitempty"`
}

type FileHistoryResult struct {
	Ref             string             `json:"ref"`
	CommitHash      string             `json:"commit_hash"`
	Path            string             `json:"path"`
	Follow          bool               `json:"follow"`
	Items           []FileHistoryEntry `json:"items"`
	HasMore         bool               `json:"has_more"`
	Limit           int                `json:"limit"`
	Offset          int                `json:"offset"`
	Shallow         bool               `json:"shallow"`
	HistoryComplete bool               `json:"history_complete"`
	Truncated       bool               `json:"truncated"`
}

func (s *Scanner) FileHistory(ctx context.Context, spec SourceSpec, ref, filePath string, follow bool, limit, offset int) (FileHistoryResult, error) {
	var result FileHistoryResult
	ref, err := NormalizeRef(ref)
	if err != nil {
		return result, err
	}
	if !validRepoPath(filePath) || !validHistoryPage(limit, offset) {
		return result, fmt.Errorf("%w: invalid repository path or pagination", domain.ErrInvalidInput)
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.GitTimeout)
	defer cancel()
	spec.Ref, spec.preferCache, spec.deepenHistory = ref, true, true
	repo, err := s.prepare(ctx, spec)
	if err != nil {
		return result, err
	}
	defer repo.Release()
	args := []string{"--git-dir=" + repo.GitDir, "log", "--no-notes", "--no-show-signature", "--encoding=UTF-8"}
	if follow {
		args = append(args, "--follow")
	}
	skip, count := offset, limit+1
	if follow {
		// Git applies --skip before rename detection, so skipped changes
		// cannot update the followed path. Walk the prefix before slicing.
		skip, count = 0, int(min(int64(math.MaxInt32), int64(offset)+int64(count)))
	}
	args = append(args, "-M", "-l256", "--name-status", "-z", "--skip="+strconv.Itoa(skip), "--max-count="+strconv.Itoa(count), commitFormat, repo.PinnedCommit, "--", filePath)
	raw, err := (gitRunner{}).run(ctx, treeOutputLimit, false, args...)
	if err != nil {
		return result, err
	}
	items, err := parseFileLog(raw, filePath)
	if err != nil {
		return result, err
	}
	if follow {
		items = items[min(offset, len(items)):]
	}
	if offset == 0 && len(items) == 0 {
		if _, err := lookupCommitPath(ctx, repo, filePath); err != nil {
			return result, err
		}
	}
	hasMore := len(items) > limit
	items = items[:min(len(items), limit)]
	for i := range items {
		if repo.SourceType == domain.RepositorySourceGitHub {
			items[i].URL = repo.CanonicalSource + "/commit/" + items[i].Hash
		}
		if repo.Shallow && len(items[i].Parents) == 0 {
			// Pretty formats hide the parents of shallow boundary commits.
			atCommit := *repo
			atCommit.PinnedCommit = items[i].Hash
			commit, err := readCommit(ctx, &atCommit)
			if err != nil {
				return result, err
			}
			items[i].Parents = commit.Parents
		}
	}
	return FileHistoryResult{
		Ref: ref, CommitHash: repo.PinnedCommit, Path: filePath, Follow: follow, Items: items,
		HasMore: hasMore, Limit: limit, Offset: offset, Shallow: repo.Shallow,
		HistoryComplete: !repo.Shallow && !hasMore, Truncated: repo.Shallow || hasMore,
	}, nil
}

func parseFileLog(raw []byte, filePath string) ([]FileHistoryEntry, error) {
	items := []FileHistoryEntry{}
	fields := strings.Split(string(raw), "\x00")
	for len(fields) > 1 {
		if len(fields) < 11 || !validOID(fields[0]) || !validOID(fields[1]) {
			return nil, errors.New("invalid Git file history commit record")
		}
		authored, authorErr := time.Parse(time.RFC3339, fields[5])
		committed, committerErr := time.Parse(time.RFC3339, fields[8])
		if authorErr != nil || committerErr != nil {
			return nil, errors.New("invalid Git file history timestamp")
		}
		item := FileHistoryEntry{
			CommitInfo: CommitInfo{
				Hash: fields[0], TreeHash: fields[1], Parents: append([]string{}, strings.Fields(fields[2])...),
				AuthorName: fields[3], AuthorEmail: fields[4], AuthoredAt: authored.UTC(),
				CommitterName: fields[6], CommitterEmail: fields[7], CommittedAt: committed.UTC(), Subject: fields[9],
			},
			Path: filePath,
		}
		fields = fields[10:]
		// The name-status section begins with a newline. A merge can have
		// no status rows. Consume status/path groups until the next OID.
		for len(fields) > 1 && !validOID(fields[0]) {
			status := strings.TrimPrefix(fields[0], "\n")
			if status == "" {
				fields = fields[1:]
				continue
			}
			if len(fields) < 3 || !strings.ContainsRune("ACDMRTUXB", rune(status[0])) {
				return nil, errors.New("invalid Git file history change record")
			}
			change, path, oldPath := status[:1], fields[1], ""
			fields = fields[2:]
			if change == "R" || change == "C" {
				if len(fields) < 2 {
					return nil, errors.New("incomplete Git file history rename record")
				}
				oldPath, path = path, fields[0]
				fields = fields[1:]
			}
			if item.ChangeType == "" || path == filePath {
				item.ChangeType, item.Path, item.OldPath = change, path, oldPath
			}
		}
		if item.OldPath != "" {
			filePath = item.OldPath
		} else {
			filePath = item.Path
		}
		items = append(items, item)
	}
	return items, nil
}

type BlameLine struct {
	Line           int       `json:"line"`
	OriginalLine   int       `json:"original_line"`
	CommitHash     string    `json:"commit_hash"`
	AuthorName     string    `json:"author_name"`
	AuthorEmail    string    `json:"author_email"`
	AuthoredAt     time.Time `json:"authored_at"`
	CommitterName  string    `json:"committer_name"`
	CommitterEmail string    `json:"committer_email"`
	CommittedAt    time.Time `json:"committed_at"`
	Summary        string    `json:"summary"`
	Filename       string    `json:"filename"`
	Boundary       bool      `json:"boundary"`
	Content        string    `json:"content"`
}

type FileBlameResult struct {
	Ref             string      `json:"ref"`
	CommitHash      string      `json:"commit_hash"`
	Path            string      `json:"path"`
	StartLine       int         `json:"start_line"`
	EndLine         int         `json:"end_line"`
	TotalLines      int         `json:"total_lines"`
	Shallow         bool        `json:"shallow"`
	BoundaryReached bool        `json:"boundary_reached"`
	HistoryComplete bool        `json:"history_complete"`
	Truncated       bool        `json:"truncated"`
	Lines           []BlameLine `json:"lines"`
}

func (s *Scanner) Blame(ctx context.Context, spec SourceSpec, ref, filePath string, startLine, endLine int) (FileBlameResult, error) {
	var result FileBlameResult
	ref, err := NormalizeRef(ref)
	if err != nil {
		return result, err
	}
	if !validRepoPath(filePath) {
		return result, fmt.Errorf("%w: invalid repository path", domain.ErrInvalidInput)
	}
	explicit := startLine != 0 || endLine != 0
	if explicit && (startLine < 1 || endLine < startLine || endLine-startLine >= BlameLineLimit) {
		return result, fmt.Errorf("%w: expected a positive line range of at most %d lines", domain.ErrInvalidInput, BlameLineLimit)
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.GitTimeout)
	defer cancel()
	spec.Ref, spec.preferCache, spec.deepenHistory = ref, true, true
	repo, err := s.prepare(ctx, spec)
	if err != nil {
		return result, err
	}
	defer repo.Release()
	file, err := s.readHistoricalFile(ctx, repo, filePath)
	if err != nil {
		return result, err
	}
	if file.Binary {
		return result, fmt.Errorf("%w: blame requires a text file", domain.ErrInvalidInput)
	}
	result = FileBlameResult{
		Ref: ref, CommitHash: repo.PinnedCommit, Path: filePath, TotalLines: file.LineCount,
		Shallow: repo.Shallow, HistoryComplete: !repo.Shallow, Lines: []BlameLine{},
	}
	if startLine > file.LineCount {
		return result, fmt.Errorf("%w: start line is past the end of the file", domain.ErrInvalidInput)
	}
	if file.LineCount == 0 {
		return result, nil
	}
	if !explicit {
		startLine, endLine = 1, min(file.LineCount, BlameLineLimit)
		result.Truncated = file.LineCount > BlameLineLimit
	}
	endLine = min(endLine, file.LineCount)
	result.StartLine, result.EndLine = startLine, endLine
	dir, git, cleanup, err := isolatedBlameRepo(repo)
	if err != nil {
		return result, err
	}
	defer cleanup()
	args := []string{"--git-dir=" + dir, "blame", "--no-textconv", "--porcelain"}
	if explicit || result.Truncated {
		args = append(args, "-L", fmt.Sprintf("%d,%d", startLine, endLine))
	}
	args = append(args, repo.PinnedCommit, "--", filePath)
	raw, err := git.run(ctx, treeOutputLimit, false, args...)
	if err != nil {
		return result, err
	}
	result.Lines, result.BoundaryReached, err = parseBlame(raw, startLine, endLine)
	result.HistoryComplete = !result.Shallow && !result.BoundaryReached
	return result, err
}

func isolatedBlameRepo(repo *PreparedRepo) (string, gitRunner, func(), error) {
	// Git opens configured ignore-revs files in order before an empty
	// override clears the accumulated revisions. Use config-free metadata
	// and the original object store for the pinned blame command.
	dir, err := os.MkdirTemp("", "projectlens-blame-")
	if err != nil {
		return "", gitRunner{}, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	fail := func(err error) (string, gitRunner, func(), error) {
		cleanup()
		return "", gitRunner{}, nil, err
	}
	if err := os.Mkdir(filepath.Join(dir, "refs"), 0700); err != nil {
		return fail(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte(repo.PinnedCommit+"\n"), 0600); err != nil {
		return fail(err)
	}
	config := "[core]\n\tbare = true\n"
	if len(repo.PinnedCommit) == 64 {
		config += "\trepositoryFormatVersion = 1\n[extensions]\n\tobjectFormat = sha256\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(config), 0600); err != nil {
		return fail(err)
	}
	commonDir := repo.CommonDir
	if commonDir == "" {
		commonDir = repo.GitDir
	}
	if repo.Shallow {
		if err := os.Symlink(filepath.Join(commonDir, "shallow"), filepath.Join(dir, "shallow")); err != nil {
			return fail(err)
		}
	}
	return dir, gitRunner{objectDir: filepath.Join(commonDir, "objects")}, cleanup, nil
}

func parseBlame(raw []byte, first, last int) ([]BlameLine, bool, error) {
	lines := make([]BlameLine, 0, last-first+1)
	cache := make(map[string]BlameLine)
	boundary := false
	for len(raw) > 0 {
		header, remaining, ok := bytes.Cut(raw, []byte{'\n'})
		fields := strings.Fields(string(header))
		if !ok || len(fields) < 3 || len(fields) > 4 || !validOID(fields[0]) {
			return nil, false, errors.New("invalid Git blame line header")
		}
		oid := fields[0]
		line := cache[oid]
		line.CommitHash = oid
		var err error
		line.OriginalLine, err = strconv.Atoi(fields[1])
		if err != nil || line.OriginalLine < 1 {
			return nil, false, errors.New("invalid Git blame original line")
		}
		line.Line, err = strconv.Atoi(fields[2])
		if err != nil || line.Line != first+len(lines) || line.Line > last {
			return nil, false, errors.New("invalid Git blame result line")
		}
		raw = remaining
		for {
			record, remaining, ok := bytes.Cut(raw, []byte{'\n'})
			if !ok {
				return nil, false, errors.New("incomplete Git blame line")
			}
			raw = remaining
			if content, ok := bytes.CutPrefix(record, []byte{'\t'}); ok {
				line.Content = string(content)
				break
			}
			key, value, _ := strings.Cut(string(record), " ")
			if err := setBlameMetadata(&line, key, value); err != nil {
				return nil, false, err
			}
		}
		boundary = boundary || line.Boundary
		lines = append(lines, line)
		line.Content = ""
		cache[oid] = line
	}
	if len(lines) != last-first+1 {
		return nil, false, errors.New("Git blame returned an unexpected number of lines")
	}
	return lines, boundary, nil
}

func setBlameMetadata(line *BlameLine, key, value string) error {
	switch key {
	case "author":
		line.AuthorName = value
	case "author-mail":
		line.AuthorEmail = strings.TrimSuffix(strings.TrimPrefix(value, "<"), ">")
	case "committer":
		line.CommitterName = value
	case "committer-mail":
		line.CommitterEmail = strings.TrimSuffix(strings.TrimPrefix(value, "<"), ">")
	case "summary":
		line.Summary = value
	case "filename":
		if strings.HasPrefix(value, "\"") {
			decoded, err := strconv.Unquote(value)
			if err != nil {
				return fmt.Errorf("invalid Git blame filename: %w", err)
			}
			value = decoded
		}
		line.Filename = value
	case "boundary":
		line.Boundary = true
	case "author-time", "committer-time":
		seconds, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid Git blame timestamp: %w", err)
		}
		if key == "author-time" {
			line.AuthoredAt = time.Unix(seconds, 0).UTC()
		} else {
			line.CommittedAt = time.Unix(seconds, 0).UTC()
		}
	}
	return nil
}

type GitRef struct {
	Type       string `json:"type"`
	Name       string `json:"name"`
	Ref        string `json:"ref"`
	ObjectHash string `json:"object_hash"`
	CommitHash string `json:"commit_hash,omitempty"`
}

type RefListResult struct {
	DefaultBranch string   `json:"default_branch"`
	Items         []GitRef `json:"items"`
	Total         int      `json:"total"`
	Limit         int      `json:"limit"`
	Offset        int      `json:"offset"`
}

func makeGitRef(ref, oid string) GitRef {
	name, branch := strings.CutPrefix(ref, "refs/heads/")
	if branch {
		return GitRef{Type: "branch", Name: name, Ref: ref, ObjectHash: oid}
	}
	return GitRef{Type: "tag", Name: strings.TrimPrefix(ref, "refs/tags/"), Ref: ref, ObjectHash: oid}
}

func (s *Scanner) ListRefs(ctx context.Context, spec SourceSpec, refType string, limit, offset int) (RefListResult, error) {
	var result RefListResult
	if refType != "" && refType != "branch" && refType != "tag" {
		return result, fmt.Errorf("%w: type must be branch or tag", domain.ErrInvalidInput)
	}
	if !validHistoryPage(limit, offset) {
		return result, fmt.Errorf("%w: invalid pagination", domain.ErrInvalidInput)
	}
	var source Source
	switch spec.SourceType {
	case domain.RepositorySourceLocalGit:
		source = s.local
	case domain.RepositorySourceGitHub:
		source = s.remote
	}
	lister, ok := source.(interface {
		ListRefs(context.Context, SourceSpec) ([]GitRef, string, error)
	})
	if !ok {
		return result, fmt.Errorf("%w: unsupported Git source type", domain.ErrInvalidInput)
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.GitTimeout)
	defer cancel()
	items, defaultBranch, err := lister.ListRefs(ctx, spec)
	if err != nil {
		return result, err
	}
	items = slices.DeleteFunc(items, func(item GitRef) bool { return refType != "" && item.Type != refType })
	slices.SortFunc(items, func(a, b GitRef) int {
		if a.Type != b.Type {
			return strings.Compare(a.Type, b.Type)
		}
		return strings.Compare(a.Name, b.Name)
	})
	start := min(offset, len(items))
	return RefListResult{
		DefaultBranch: defaultBranch, Items: items[start:min(start+limit, len(items))],
		Total: len(items), Limit: limit, Offset: offset,
	}, nil
}
