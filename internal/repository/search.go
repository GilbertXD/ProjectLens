package repository

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"projectlens/internal/db"
	"projectlens/internal/domain"
)

type PostgresSearchStore struct {
	pool *pgxpool.Pool
}

func NewPostgresSearchStore(pool *pgxpool.Pool) *PostgresSearchStore {
	return &PostgresSearchStore{pool: pool}
}

func (s *PostgresSearchStore) GetProjectSearchVersion(ctx context.Context, projectID uuid.UUID) (int64, error) {
	var version int64
	err := db.ConnFromContext(ctx, s.pool).QueryRow(ctx, "SELECT search_version FROM projects WHERE id = $1", projectID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, domain.ErrNotFound
	}
	return version, err
}

func (s *PostgresSearchStore) ValidateRepositoryOwnership(ctx context.Context, projectID uuid.UUID, repositoryIDs []uuid.UUID) error {
	var valid bool
	err := db.ConnFromContext(ctx, s.pool).QueryRow(ctx, `
		SELECT NOT EXISTS (
			SELECT 1 FROM unnest($2::uuid[]) AS requested(id)
			WHERE NOT EXISTS (SELECT 1 FROM repositories WHERE project_id = $1 AND id = requested.id)
		)`, projectID, repositoryIDs).Scan(&valid)
	if err == nil && !valid {
		return domain.ErrNotFound
	}
	return err
}

// Materialize content only for page rows and calculate line numbers in PostgreSQL.
const unifiedSearchSQL = `
WITH q AS NOT MATERIALIZED (
    SELECT websearch_to_tsquery('simple', $2) AS tsq
), matching_repositories AS NOT MATERIALIZED (
    SELECT id, source_type FROM repositories
    WHERE project_id = $1
      AND ($4::uuid IS NULL OR id = $4)
      AND (COALESCE(cardinality($12::uuid[]), 0) = 0 OR id = ANY($12))
      AND ($13::text = '' OR role = $13)
), source_matches AS (
    SELECT sf.id, 'source_file' AS kind, ts_rank_cd(sf.search_vector, q.tsq, 32) AS score,
           COALESCE(s.published_at, sf.created_at) AS updated_at
    FROM source_files sf
    JOIN snapshots s ON s.id = sf.snapshot_id AND s.repository_id = sf.repository_id AND s.status = 'active'
    JOIN matching_repositories r ON r.id = s.repository_id
    CROSS JOIN q
    WHERE q.tsq <> ''::tsquery AND sf.search_vector @@ q.tsq
      AND (cardinality($3::text[]) = 0 OR r.source_type = ANY($3))
      AND $11::text <> 'project'
      AND ($5::text = '' OR sf.path LIKE $5 ESCAPE E'\\')
), doc_matches AS (
    SELECT d.id, 'document' AS kind, ts_rank_cd(d.search_vector, q.tsq, 32) AS score, d.updated_at
    FROM documents d CROSS JOIN q
    WHERE d.project_id = $1 AND q.tsq <> ''::tsquery AND d.search_vector @@ q.tsq
      AND (cardinality($3::text[]) = 0 OR 'document' = ANY($3))
      AND $5::text = ''
      AND CASE $11::text
          WHEN 'all' THEN true
          WHEN 'project' THEN d.repository_id IS NULL
          WHEN 'repository' THEN d.repository_id = $4
          WHEN 'cross_repository' THEN
              d.repository_id IN (SELECT id FROM matching_repositories)
              OR ($14::boolean AND d.repository_id IS NULL)
          ELSE false
      END
), unified AS (
    SELECT * FROM source_matches UNION ALL SELECT * FROM doc_matches
), page AS (
    SELECT * FROM unified ORDER BY score DESC, updated_at DESC, kind ASC, id ASC LIMIT $6 OFFSET $7
), totals AS (
    SELECT count(*) AS total FROM unified
)
SELECT p.search_version, totals.total, page.id, page.kind, page.score::double precision, page.updated_at,
       CASE WHEN sf.id IS NOT NULL THEN r.source_type ELSE 'document' END, COALESCE(sf.path, d.title),
       COALESCE(snippet.text, ''),
       1 + length(first_match.prefix) - length(replace(first_match.prefix, E'\n', '')) AS match_line,
       CASE WHEN sf.id IS NOT NULL AND strpos(snippet.text, $10) = 0 THEN ARRAY(
           SELECT part.ordinality::integer
           FROM regexp_matches(sf.path, '[^/._-]+', 'g') WITH ORDINALITY AS part(tokens, ordinality)
           WHERE strpos(ts_headline('simple', part.tokens[1], q.tsq, $8), $10) > 0
           ORDER BY part.ordinality
       ) END AS matched_path_segments,
       r.id, COALESCE(r.name, ''), COALESCE(r.source, ''), COALESCE(r.role, ''),
       s.id, s.version, COALESCE(s.commit_hash, ''),
       COALESCE(r.github_owner, ''), COALESCE(r.github_repo, ''), d.version
FROM projects p CROSS JOIN totals CROSS JOIN q
LEFT JOIN page ON true
LEFT JOIN source_files sf ON page.kind = 'source_file' AND page.id = sf.id
LEFT JOIN snapshots s ON s.id = sf.snapshot_id
LEFT JOIN documents d ON page.kind = 'document' AND page.id = d.id
LEFT JOIN repositories r ON r.id = COALESCE(s.repository_id, d.repository_id) AND r.project_id = p.id
CROSS JOIN LATERAL (SELECT COALESCE(sf.content, d.content) AS content) body
LEFT JOIN LATERAL (SELECT ts_headline('simple', body.content, q.tsq, $8) AS body OFFSET 0) headline ON true
LEFT JOIN LATERAL (
    SELECT CASE WHEN strpos(headline.body, $10) > 0 THEN headline.body
                WHEN sf.id IS NOT NULL THEN ts_headline('simple', sf.path, q.tsq, $8)
                ELSE ts_headline('simple', d.title, q.tsq, $8)
           END AS text
    OFFSET 0
) snippet ON true
LEFT JOIN LATERAL (
    SELECT CASE WHEN strpos(headline.body, $10) > 0 THEN ts_headline('simple', body.content, q.tsq, $9) END AS body
    OFFSET 0
) highlighted ON true
LEFT JOIN LATERAL (
    SELECT left(highlighted.body, NULLIF(strpos(highlighted.body, $10), 0) - 1) AS prefix
    OFFSET 0
) first_match ON true
WHERE p.id = $1
  AND NOT EXISTS (
      SELECT 1
      FROM (SELECT $4::uuid AS id WHERE $4 IS NOT NULL UNION ALL SELECT unnest($12::uuid[])) requested
      WHERE NOT EXISTS (SELECT 1 FROM repositories WHERE project_id = p.id AND id = requested.id)
  )
ORDER BY page.score DESC, page.updated_at DESC, page.kind ASC, page.id ASC`

func (s *PostgresSearchStore) Search(ctx context.Context, projectID uuid.UUID, filter domain.SearchFilter) (domain.SearchResult, error) {
	result := domain.SearchResult{
		Query: filter.Query, Limit: filter.Limit, Offset: filter.Offset, Items: []domain.SearchHit{},
	}
	sourceTypes := make([]string, len(filter.SourceTypes))
	for i, source := range filter.SourceTypes {
		sourceTypes[i] = string(source)
	}
	var pathPattern string
	if filter.PathPrefix != "" {
		pathPattern = strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(filter.PathPrefix) + "%"
	}

	// Escape source text for HTML before replacing the per-query markers with mark tags.
	token := uuid.NewString()
	startMark, stopMark := token+"-start", token+"-stop"
	markers := "StartSel=" + startMark + ", StopSel=" + stopMark
	snippetOptions := markers + `, MaxFragments=2, MaxWords=35, MinWords=15, FragmentDelimiter=" ... "`
	rows, err := db.ConnFromContext(ctx, s.pool).Query(ctx, unifiedSearchSQL, projectID, filter.Query,
		sourceTypes, filter.RepositoryID, pathPattern, filter.Limit, filter.Offset,
		snippetOptions, markers+", HighlightAll=true", startMark,
		filter.EffectiveScope(), filter.RepositoryIDs, filter.RepositoryRole, filter.IncludeProjectDocuments)
	if err != nil {
		return domain.SearchResult{}, err
	}
	defer rows.Close()

	escapedMarkers := strings.NewReplacer(startMark, "<mark>", stopMark, "</mark>")
	for rows.Next() {
		hit := domain.SearchHit{ProjectID: projectID}
		loc := &hit.Location
		var id *uuid.UUID
		var kind, title *string
		var score *float64
		var updatedAt *time.Time
		var snippet, owner, repo string
		var matchedPathSegments []int32
		err := rows.Scan(&result.SearchVersion, &result.Total, &id, &kind, &score, &updatedAt,
			&hit.SourceType, &title, &snippet, &loc.Line, &matchedPathSegments,
			&loc.RepositoryID, &loc.RepositoryName, &loc.RepositorySource, &loc.RepositoryRole, &loc.SnapshotID,
			&loc.SnapshotVersion, &loc.CommitHash, &owner, &repo, &loc.DocumentVersion)
		if err != nil {
			return domain.SearchResult{}, err
		}
		if id == nil {
			continue
		}
		hit.ID, hit.Kind, hit.Title = *id, domain.SearchHitKind(*kind), *title
		hit.Score, hit.UpdatedAt = *score, *updatedAt
		if matchedPathSegments != nil {
			hit.Snippet = highlightPathSegments(hit.Title, matchedPathSegments)
		} else {
			hit.Snippet = escapedMarkers.Replace(html.EscapeString(snippet))
		}
		setSearchLocation(&hit, owner, repo)
		result.Items = append(result.Items, hit)
	}
	if err := rows.Err(); err != nil {
		return domain.SearchResult{}, err
	}
	if result.SearchVersion == 0 {
		return domain.SearchResult{}, domain.ErrNotFound
	}
	return result, nil
}

// Use the same segment boundaries as the fallback SQL and keep the original separators.
var searchPathSegments = regexp.MustCompile(`[^/._-]+`)

func highlightPathSegments(path string, matches []int32) string {
	segments := searchPathSegments.FindAllStringIndex(path, -1)
	var snippet strings.Builder
	end := 0
	for _, match := range matches {
		segment := segments[match-1]
		snippet.WriteString(html.EscapeString(path[end:segment[0]]))
		snippet.WriteString("<mark>")
		snippet.WriteString(html.EscapeString(path[segment[0]:segment[1]]))
		snippet.WriteString("</mark>")
		end = segment[1]
	}
	snippet.WriteString(html.EscapeString(path[end:]))
	return snippet.String()
}

func setSearchLocation(hit *domain.SearchHit, owner, repo string) {
	loc := &hit.Location
	scopePath := "/api/v1/projects/" + hit.ProjectID.String()
	loc.Scope = domain.DocumentScope(loc.RepositoryID)
	if loc.RepositoryID != nil {
		scopePath += "/repositories/" + loc.RepositoryID.String()
	}
	if hit.Kind == domain.SearchHitKindDocument {
		loc.DocumentID = &hit.ID
		loc.DocumentTitle = hit.Title
		loc.APIPath = scopePath + "/documents/" + hit.ID.String()
		return
	}
	loc.Path = hit.Title
	loc.APIPath = scopePath + "/files/content?path=" + url.QueryEscape(loc.Path)
	if hit.SourceType == domain.SearchSourceGitHub {
		segments := strings.Split(loc.Path, "/")
		for i, segment := range segments {
			segments[i] = url.PathEscape(segment)
		}
		loc.URL = fmt.Sprintf("https://github.com/%s/%s/blob/%s/%s",
			url.PathEscape(owner), url.PathEscape(repo), loc.CommitHash, strings.Join(segments, "/"))
		if loc.Line != nil {
			loc.URL += "#L" + strconv.Itoa(*loc.Line)
		}
	}
}
