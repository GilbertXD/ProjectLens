package gitscan

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"projectlens/internal/config"
	"projectlens/internal/domain"
)

const (
	skippedLimit    = 2000
	treeOutputLimit = 64 << 20
)

type Scanner struct {
	cfg    config.ScannerConfig
	local  Source
	remote Source
}

func NewScanner(cfg config.ScannerConfig, repository config.RepositoryConfig, github config.GitHubConfig) *Scanner {
	cfg.ExcludePatterns = slices.Clone(cfg.ExcludePatterns)
	return &Scanner{cfg: cfg, local: NewLocalSource(repository.AllowedLocalRoots), remote: NewRemoteSource(cfg.CacheDir, github.Token, github.GitBaseURL)}
}

func (s *Scanner) prepare(ctx context.Context, spec SourceSpec) (*PreparedRepo, error) {
	switch spec.SourceType {
	case domain.RepositorySourceLocalGit:
		return s.local.Prepare(ctx, spec)
	case domain.RepositorySourceGitHub:
		return s.remote.Prepare(ctx, spec)
	default:
		return nil, fmt.Errorf("%w: unsupported Git source type", domain.ErrInvalidInput)
	}
}

func (s *Scanner) Scan(ctx context.Context, spec SourceSpec, options ScanOptions) (*Snapshot, error) {
	if err := s.cfg.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.GitTimeout)
	defer cancel()
	repo, err := s.prepare(ctx, spec)
	if err != nil {
		return nil, err
	}
	defer repo.Release()
	commit, err := readCommit(ctx, repo)
	if err != nil {
		return nil, err
	}
	entries, err := readTree(ctx, repo)
	if err != nil {
		return nil, err
	}
	snapshot := &Snapshot{
		SourceType: repo.SourceType, CanonicalSource: repo.CanonicalSource, ResolvedRef: repo.ResolvedRef,
		Commit: commit, Files: []FileEntry{}, Skipped: []SkippedFile{}, ScannedAt: time.Now().UTC(),
		Stats: SnapshotStats{TotalTreeEntries: len(entries), SkippedByReason: make(map[string]int)},
	}
	var batch *blobBatch
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		reason := filterEntry(entry, s.cfg)
		switch {
		case reason != "":
		case len(snapshot.Files) >= s.cfg.MaxFiles:
			reason = SkipMaxFilesExceeded
		case entry.Size > s.cfg.MaxTotalBytes-snapshot.Stats.IncludedBytes:
			reason = SkipTotalBytesExceeded
		default:
			if batch == nil {
				batch, err = startBlobBatch(ctx, repo.GitDir)
				if err != nil {
					return nil, err
				}
				defer batch.abort()
			}
			content, err := batch.read(entry.BlobOID, entry.Size, s.cfg.MaxFileSize)
			switch {
			case errors.Is(err, errBlobUnavailable):
				reason = SkipBlobReadError
			case err != nil:
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, err
			case isBinary(content):
				reason = SkipBinaryFile
			default:
				snapshot.include(entry, content, options)
			}
		}
		if reason != "" {
			snapshot.skip(entry, reason)
		}
	}
	if batch != nil {
		if err := batch.finish(ctx); err != nil {
			return nil, err
		}
	}
	return snapshot, ctx.Err()
}

func (s *Snapshot) include(entry treeEntry, content []byte, options ScanOptions) {
	s.Files = append(s.Files, describeFile(entry, content, options))
	s.Stats.IncludedFiles++
	s.Stats.IncludedBytes += entry.Size
}

func describeFile(entry treeEntry, content []byte, options ScanOptions) FileEntry {
	lines := bytes.Count(content, []byte{'\n'})
	if len(content) > 0 && content[len(content)-1] != '\n' {
		lines++
	}
	file := FileEntry{
		Path: entry.Path, BlobOID: entry.BlobOID, Mode: entry.Mode, Size: entry.Size,
		SHA256: fmt.Sprintf("%x", sha256.Sum256(content)), LineCount: lines, IsExecutable: entry.Mode == "100755",
	}
	if options.IncludeContent {
		file.Content = content
	}
	return file
}

func (s *Snapshot) skip(entry treeEntry, reason SkipReason) {
	recordSkip(&s.Stats, &s.Skipped, entry, reason)
}

func recordSkip(stats *SnapshotStats, skipped *[]SkippedFile, entry treeEntry, reason SkipReason) {
	stats.SkippedFiles++
	stats.SkippedByReason[string(reason)]++
	if reason == SkipMaxFilesExceeded || reason == SkipTotalBytesExceeded {
		stats.Truncated = true
	}
	if len(*skipped) == skippedLimit {
		stats.SkippedListTruncated = true
		return
	}
	entry.Reason = reason
	*skipped = append(*skipped, entry.SkippedFile)
}

// ReadBlob reads an object referenced by a regular file in the requested commit.
// A nonempty commitOrRef overrides spec.Ref.
func (s *Scanner) ReadBlob(ctx context.Context, spec SourceSpec, commitOrRef, blobOID string) ([]byte, error) {
	if err := s.cfg.Validate(); err != nil {
		return nil, err
	}
	if !validOID(blobOID) {
		return nil, fmt.Errorf("%w: invalid blob ID", domain.ErrInvalidInput)
	}
	if commitOrRef != "" {
		spec.Ref = commitOrRef
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.GitTimeout)
	defer cancel()
	repo, err := s.prepare(ctx, spec)
	if err != nil {
		return nil, err
	}
	defer repo.Release()
	entries, err := readTree(ctx, repo)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.BlobOID != blobOID || entry.kind != "blob" ||
			(entry.Mode != "100644" && entry.Mode != "100755") {
			continue
		}
		if entry.Size > s.cfg.MaxFileSize {
			return nil, fmt.Errorf("%w: blob exceeds the maximum file size", domain.ErrInvalidInput)
		}
		batch, err := startBlobBatch(ctx, repo.GitDir)
		if err != nil {
			return nil, err
		}
		defer batch.abort()
		content, err := batch.read(blobOID, entry.Size, s.cfg.MaxFileSize)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, errBlobUnavailable) {
			return nil, fmt.Errorf("%w: blob is unavailable", domain.ErrNotFound)
		}
		if err != nil {
			return nil, err
		}
		if err := batch.finish(ctx); err != nil {
			return nil, err
		}
		return content, nil
	}
	return nil, fmt.Errorf("%w: blob is not a regular file in the pinned commit", domain.ErrNotFound)
}

func readCommit(ctx context.Context, repo *PreparedRepo) (CommitInfo, error) {
	git := gitRunner{}
	dir := "--git-dir=" + repo.GitDir
	kind, err := git.run(ctx, 1024, false, dir, "cat-file", "-t", repo.PinnedCommit)
	if err != nil {
		return CommitInfo{}, err
	}
	if string(kind) != "commit\n" {
		return CommitInfo{}, errors.New("pinned Git object is not a commit")
	}
	const format = "--format=%H%x00%T%x00%P%x00%an%x00%ae%x00%aI%x00%cn%x00%ce%x00%cI%x00%s"
	raw, err := git.run(ctx, 1<<20, false, dir, "show", "-s", "--no-show-signature", "--no-notes", "--encoding=UTF-8", format, repo.PinnedCommit)
	if err != nil {
		return CommitInfo{}, err
	}
	fields := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\x00")
	if len(fields) != 10 || fields[0] != repo.PinnedCommit || !validOID(fields[1]) {
		return CommitInfo{}, errors.New("invalid Git commit metadata")
	}
	authored, err := time.Parse(time.RFC3339, fields[5])
	if err != nil {
		return CommitInfo{}, fmt.Errorf("invalid Git author date: %w", err)
	}
	committed, err := time.Parse(time.RFC3339, fields[8])
	if err != nil {
		return CommitInfo{}, fmt.Errorf("invalid Git committer date: %w", err)
	}
	// Pretty-format parents are affected by shallow boundaries and grafts.
	// Read parent IDs from the commit object so the manifest is source-independent.
	object, err := git.run(ctx, 1<<20, false, dir, "cat-file", "commit", repo.PinnedCommit)
	if err != nil {
		return CommitInfo{}, err
	}
	headers, _, _ := bytes.Cut(object, []byte("\n\n"))
	parents := []string{}
	for line := range bytes.SplitSeq(headers, []byte{'\n'}) {
		if oid, ok := bytes.CutPrefix(line, []byte("parent ")); ok {
			if !validOID(string(oid)) {
				return CommitInfo{}, errors.New("invalid Git commit parent ID")
			}
			parents = append(parents, string(oid))
		}
	}
	return CommitInfo{
		Hash: fields[0], TreeHash: fields[1], Parents: parents,
		AuthorName: fields[3], AuthorEmail: fields[4], AuthoredAt: authored.UTC(),
		CommitterName: fields[6], CommitterEmail: fields[7], CommittedAt: committed.UTC(), Subject: fields[9],
	}, nil
}

type treeEntry struct {
	SkippedFile
	kind string
}

func readTree(ctx context.Context, repo *PreparedRepo) ([]treeEntry, error) {
	raw, err := (gitRunner{}).run(ctx, treeOutputLimit, false, "--git-dir="+repo.GitDir, "ls-tree", "-r", "-l", "-z", repo.PinnedCommit)
	if err != nil {
		return nil, err
	}
	var entries []treeEntry
	for len(raw) > 0 {
		record, remaining, terminated := bytes.Cut(raw, []byte{0})
		header, name, separated := bytes.Cut(record, []byte{'\t'})
		fields := strings.Fields(string(header))
		if !terminated || !separated || len(fields) != 4 || !validOID(fields[2]) {
			return nil, errors.New("invalid Git tree listing")
		}
		var size int64
		if fields[3] != "-" && fields[3] != "BAD" {
			size, err = strconv.ParseInt(fields[3], 10, 64)
			if err != nil || size < 0 {
				return nil, errors.New("invalid Git tree object size")
			}
		} else if fields[1] == "blob" {
			// Git reports a missing blob with an invalid size. Do not read it.
			size = -1
		}
		entries = append(entries, treeEntry{
			SkippedFile: SkippedFile{Path: string(name), Mode: fields[0], BlobOID: fields[2], Size: size},
			kind:        fields[1],
		})
		raw = remaining
	}
	slices.SortFunc(entries, func(a, b treeEntry) int { return strings.Compare(a.Path, b.Path) })
	return entries, nil
}

var errBlobUnavailable = errors.New("Git blob is missing or inconsistent with the tree")

type blobBatch struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	cancel context.CancelFunc
	waited bool
}

func startBlobBatch(ctx context.Context, dir string) (*blobBatch, error) {
	ctx, cancel := context.WithCancel(ctx)
	cmd := (gitRunner{}).command(ctx, false, "--git-dir="+dir, "cat-file", "--batch")
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = in.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		_ = in.Close()
		_ = out.Close()
		return nil, err
	}
	return &blobBatch{cmd: cmd, in: in, out: bufio.NewReader(out), cancel: cancel}, nil
}

func (b *blobBatch) read(oid string, expected, maxSize int64) ([]byte, error) {
	if expected < 0 {
		return nil, errBlobUnavailable
	}
	if _, err := io.WriteString(b.in, oid+"\n"); err != nil {
		return nil, fmt.Errorf("write Git blob request: %w", err)
	}
	header, err := b.out.ReadSlice('\n')
	if err != nil {
		return nil, fmt.Errorf("read Git blob header: %w", err)
	}
	fields := strings.Fields(string(header))
	if len(fields) == 2 && fields[0] == oid && fields[1] == "missing" {
		return nil, errBlobUnavailable
	}
	if len(fields) != 3 {
		return nil, errors.New("invalid Git batch header")
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || size < 0 {
		return nil, errors.New("invalid Git batch object size")
	}
	var content []byte
	matches := fields[0] == oid && fields[1] == "blob" && size == expected && size <= maxSize && size <= int64(int(^uint(0)>>1))
	if matches {
		// Grow only as bytes actually arrive. A corrupt object may advertise a
		// large size and terminate before writing its payload.
		var buffer bytes.Buffer
		_, err = io.CopyN(&buffer, b.out, size)
		content = buffer.Bytes()
		if content == nil {
			content = []byte{}
		}
	} else {
		_, err = io.CopyN(io.Discard, b.out, size)
	}
	if err != nil {
		return nil, fmt.Errorf("read Git blob payload: %w", err)
	}
	if end, err := b.out.ReadByte(); err != nil || end != '\n' {
		return nil, errors.New("invalid Git batch object terminator")
	}
	if !matches {
		return nil, errBlobUnavailable
	}
	return content, nil
}

func (b *blobBatch) finish(ctx context.Context) error {
	_ = b.in.Close()
	err := b.cmd.Wait()
	b.waited = true
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("finish Git blob reader: %w", err)
	}
	return nil
}

func (b *blobBatch) abort() {
	b.cancel()
	if !b.waited {
		_ = b.in.Close()
		_ = b.cmd.Wait()
	}
}

// ConfigHash identifies the filters and inclusion budgets used to build a snapshot.
func (s *Scanner) ConfigHash() string {
	patterns := append([]string{}, s.cfg.ExcludePatterns...)
	slices.Sort(patterns)
	fields := struct {
		MaxFileSize     int64
		MaxTotalBytes   int64
		MaxFiles        int
		MaxPathLength   int
		MaxPathDepth    int
		ExcludePatterns []string
	}{
		s.cfg.MaxFileSize, s.cfg.MaxTotalBytes, s.cfg.MaxFiles, s.cfg.MaxPathLength, s.cfg.MaxPathDepth, patterns,
	}
	// All fields have JSON representations that cannot fail to marshal.
	encoded, _ := json.Marshal(fields)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func (s *Scanner) ScanIncremental(ctx context.Context, spec SourceSpec, base *IncrementalBase, options ScanOptions) (*IncrementalScanResult, error) {
	if err := s.cfg.Validate(); err != nil {
		return nil, err
	}
	if !utf8.ValidString(spec.Ref) || utf8.RuneCountInString(spec.Ref) > 255 {
		return nil, fmt.Errorf("%w: Git ref must be valid UTF-8 and at most 255 characters", domain.ErrInvalidInput)
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.GitTimeout)
	defer cancel()
	repo, err := s.prepare(ctx, spec)
	if err != nil {
		return nil, err
	}
	defer repo.Release()
	commit, err := readCommit(ctx, repo)
	if err != nil {
		return nil, err
	}
	result := &IncrementalScanResult{
		ResolvedRef: repo.ResolvedRef, Commit: commit, ScannerConfigHash: s.ConfigHash(),
		ScannedAt: time.Now().UTC(),
	}
	if base != nil && base.ScannerConfigHash == result.ScannerConfigHash &&
		base.CommitHash == commit.Hash && base.TreeHash == commit.TreeHash {
		result.Unchanged = true
		result.UnchangedFiles = len(base.Files)
		return result, ctx.Err()
	}

	entries, err := readTree(ctx, repo)
	if err != nil {
		return nil, err
	}
	result.Stats = SnapshotStats{TotalTreeEntries: len(entries), SkippedByReason: make(map[string]int)}
	result.Files = []IncrementalFile{}
	result.Skipped = []SkippedFile{}
	result.DeletedPaths = []string{}

	var previous map[string]PreviousFile
	if base != nil {
		previous = base.Files
	}
	type blobKey struct {
		oid  string
		size int64
	}
	byBlob := make(map[blobKey]string, len(previous))
	for name, file := range previous {
		key := blobKey{file.BlobOID, file.Size}
		if current, ok := byBlob[key]; !ok || name < current {
			byBlob[key] = name
		}
	}
	included := make(map[string]struct{}, len(previous))
	var batch *blobBatch
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		reason := filterEntry(entry, s.cfg)
		switch {
		case reason != "":
		case utf8.RuneCountInString(entry.Path) > 512:
			reason = SkipPathLimitExceeded
		case len(result.Files) >= s.cfg.MaxFiles:
			reason = SkipMaxFilesExceeded
		case entry.Size > s.cfg.MaxTotalBytes-result.Stats.IncludedBytes:
			reason = SkipTotalBytesExceeded
		}
		if reason != "" {
			recordSkip(&result.Stats, &result.Skipped, entry, reason)
			continue
		}

		old, existed := previous[entry.Path]
		sameBlob := existed && old.BlobOID == entry.BlobOID && old.Size == entry.Size
		sourcePath, reused := byBlob[blobKey{entry.BlobOID, entry.Size}]
		if sameBlob {
			sourcePath = entry.Path
		}
		file := IncrementalFile{Reused: reused, ReusedFromPath: sourcePath}
		if reused {
			source := previous[sourcePath]
			file.FileEntry = FileEntry{
				Path: entry.Path, BlobOID: entry.BlobOID, Mode: entry.Mode, Size: entry.Size,
				SHA256: source.SHA256, LineCount: source.LineCount, IsExecutable: entry.Mode == "100755",
			}
		} else {
			if batch == nil {
				batch, err = startBlobBatch(ctx, repo.GitDir)
				if err != nil {
					return nil, err
				}
				defer batch.abort()
			}
			result.BlobsRead++
			content, err := batch.read(entry.BlobOID, entry.Size, s.cfg.MaxFileSize)
			switch {
			case ctx.Err() != nil:
				return nil, ctx.Err()
			case errors.Is(err, errBlobUnavailable):
				recordSkip(&result.Stats, &result.Skipped, entry, SkipBlobReadError)
				continue
			case err != nil:
				return nil, err
			case isBinary(content):
				recordSkip(&result.Stats, &result.Skipped, entry, SkipBinaryFile)
				continue
			}
			file.FileEntry = describeFile(entry, content, options)
		}
		result.Files = append(result.Files, file)
		result.Stats.IncludedFiles++
		result.Stats.IncludedBytes += entry.Size
		included[entry.Path] = struct{}{}
		switch {
		case !existed:
			result.AddedFiles++
		case sameBlob && old.Mode == entry.Mode:
			result.UnchangedFiles++
		default:
			result.ModifiedFiles++
		}
	}
	if batch != nil {
		if err := batch.finish(ctx); err != nil {
			return nil, err
		}
	}
	for name := range previous {
		if _, ok := included[name]; !ok {
			result.DeletedPaths = append(result.DeletedPaths, name)
		}
	}
	slices.Sort(result.DeletedPaths)
	result.DeletedFiles = len(result.DeletedPaths)
	return result, ctx.Err()
}
