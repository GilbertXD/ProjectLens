package gitscan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"projectlens/internal/domain"
)

const HistoryPage = 100
const HistoryLimit = 2000

type Change struct {
	Path       string `json:"path"`
	OldPath    string `json:"old_path,omitempty"`
	Type       string `json:"type"`
	Binary     bool   `json:"binary"`
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
}
type Changes struct {
	Patch          string   `json:"patch,omitempty"`
	PatchTruncated bool     `json:"patch_truncated,omitempty"`
	Files          []Change `json:"files"`
	Truncated      bool     `json:"truncated"`
}
type HistoryCommit struct {
	CommitInfo
	Message   string `json:"message"`
	URL       string `json:"html_url,omitempty"`
	Truncated bool   `json:"truncated"`
}
type HistoryPageResult struct {
	Tip       string   `json:"tip"`
	Reachable []string `json:"-"`
	HasMore   bool     `json:"has_more"`
	Imported  int      `json:"imported"`
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
	result.Tip = repo.PinnedCommit
	result.Reachable = ids
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
		changes, err := readChanges(ctx, repo.GitDir, base, sha)
		if err != nil {
			return result, err
		}
		if err := save(item, changes); err != nil {
			return result, err
		}
		result.Imported++
	}
	return result, nil
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
