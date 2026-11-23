package gitscan

import (
	"bytes"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"projectlens/internal/config"
)

var ignoredSegments = map[string]struct{}{
	".git": {}, "node_modules": {}, "vendor": {}, ".venv": {}, "venv": {},
	"__pycache__": {}, "dist": {}, "build": {}, "target": {}, ".DS_Store": {},
}

func filterEntry(entry treeEntry, cfg config.ScannerConfig) SkipReason {
	switch entry.Mode {
	case "120000":
		return SkipSymlink
	case "160000":
		return SkipSubmodule
	}
	if entry.kind != "blob" || (entry.Mode != "100644" && entry.Mode != "100755") {
		return SkipUnsupportedEntry
	}
	name := entry.Path
	if !utf8.ValidString(name) || strings.ContainsRune(name, '\\') || strings.ContainsFunc(name, unicode.IsControl) {
		return SkipUnsafePath
	}
	segments := strings.Split(name, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." || strings.EqualFold(segment, ".git") {
			return SkipUnsafePath
		}
	}
	if len(name) > cfg.MaxPathLength || len(segments) > cfg.MaxPathDepth {
		return SkipPathLimitExceeded
	}
	for _, segment := range segments {
		if _, ignored := ignoredSegments[segment]; ignored {
			return SkipIgnoredPath
		}
	}
	for _, pattern := range cfg.ExcludePatterns {
		target := name
		if !strings.ContainsRune(pattern, '/') {
			for _, segment := range segments {
				if matched, _ := path.Match(pattern, segment); matched {
					return SkipIgnoredPath
				}
			}
			continue
		}
		for {
			if matched, _ := path.Match(pattern, target); matched {
				return SkipIgnoredPath
			}
			end := strings.LastIndexByte(target, '/')
			if end < 0 {
				break
			}
			target = target[:end]
		}
	}
	if entry.Size < 0 {
		return SkipBlobReadError
	}
	if entry.Size > cfg.MaxFileSize {
		return SkipFileTooLarge
	}
	return ""
}

func isBinary(data []byte) bool {
	return bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data)
}
