package gitscan

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"projectlens/internal/domain"
)

type Source interface {
	Prepare(context.Context, SourceSpec) (*PreparedRepo, error)
}

func scanRef(ref string) (string, error) {
	if ref == "" {
		return "HEAD", nil
	}
	// Fetch refspec metacharacters and revision expressions are excluded as well.
	if strings.HasPrefix(ref, "-") || strings.HasPrefix(ref, "+") ||
		strings.ContainsAny(ref, ":*?[]\\^~") ||
		strings.Contains(ref, "..") || strings.Contains(ref, "@{") ||
		strings.ContainsFunc(ref, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", fmt.Errorf("%w: invalid Git ref", domain.ErrInvalidInput)
	}
	return ref, nil
}

func validOID(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}
	return strings.IndexFunc(oid, func(r rune) bool {
		return !(r >= 'a' && r <= 'f' || r >= '0' && r <= '9')
	}) == -1
}
