package repository

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"projectlens/internal/db"
	"projectlens/internal/domain"
)

type PostgresSnapshotStore struct {
	pool     *pgxpool.Pool
	lockPool *pgxpool.Pool
}

func NewPostgresSnapshotStore(ctx context.Context, pool *pgxpool.Pool) (*PostgresSnapshotStore, error) {
	// Lock holders and waiters pin sessions. Give them their own pool so they
	// cannot exhaust the connections needed to publish or serve readers.
	cfg := pool.Config()
	cfg.MinConns, cfg.MinIdleConns = 0, 0
	lockPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL sync lock pool: %w", err)
	}
	return &PostgresSnapshotStore{pool: pool, lockPool: lockPool}, nil
}

func (s *PostgresSnapshotStore) Close() { s.lockPool.Close() }

func (s *PostgresSnapshotStore) AcquireRepoSyncLock(ctx context.Context, repoID uuid.UUID) (func(), error) {
	return s.AcquireScopedSyncLock(ctx, "", repoID)
}

func (s *PostgresSnapshotStore) AcquireScopedSyncLock(ctx context.Context, scope string, repoID uuid.UUID) (func(), error) {
	key := "projectlens:repo-sync:"
	if scope != "" {
		key += scope + ":"
	}
	hash := sha256.Sum256([]byte(key + repoID.String()))
	first := int32(binary.BigEndian.Uint32(hash[:4]))
	second := int32(binary.BigEndian.Uint32(hash[4:8]))
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		conn, err := s.lockPool.Acquire(ctx)
		if err != nil {
			return nil, fmt.Errorf("acquire repository lock connection: %w", err)
		}
		var locked bool
		if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, $2)", first, second).Scan(&locked); err != nil {
			// PostgreSQL may have acquired the lock before delivery of a query error.
			discardLockConnection(conn)
			return nil, fmt.Errorf("acquire repository sync lock: %w", err)
		}
		if locked {
			return sync.OnceFunc(func() {
				unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()
				var unlocked bool
				err := conn.QueryRow(unlockCtx, "SELECT pg_advisory_unlock($1, $2)", first, second).Scan(&unlocked)
				if err != nil || !unlocked {
					discardLockConnection(conn)
					return
				}
				conn.Release()
			}), nil
		}
		conn.Release()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func discardLockConnection(conn *pgxpool.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = conn.Hijack().Close(ctx)
}

const snapshotColumns = `id, repository_id, job_id, parent_snapshot_id, version, status,
 resolved_ref, commit_hash, tree_hash, scanner_config_hash, commit_author_name, commit_author_email,
 commit_authored_at, commit_committer_name, commit_committer_email, commit_committed_at, commit_subject, commit_parents,
 total_tree_entries, included_files, included_bytes, skipped_files, skipped_by_reason, truncated,
 added_files, modified_files, deleted_files, unchanged_files, blobs_read,
 error_message, scanned_at, published_at, superseded_at, created_at, updated_at`

const fileColumns = `id, snapshot_id, repository_id, path, blob_oid, mode, size_bytes, sha256, line_count,
 is_executable, created_at`

func snapshotDestinations(s *domain.Snapshot) []any {
	return []any{&s.ID, &s.RepositoryID, &s.JobID, &s.ParentSnapshotID, &s.Version, &s.Status,
		&s.ResolvedRef, &s.CommitHash, &s.TreeHash, &s.ScannerConfigHash, &s.CommitAuthorName, &s.CommitAuthorEmail,
		&s.CommitAuthoredAt, &s.CommitCommitterName, &s.CommitCommitterEmail, &s.CommitCommittedAt,
		&s.CommitSubject, &s.CommitParents, &s.TotalTreeEntries, &s.IncludedFiles, &s.IncludedBytes,
		&s.SkippedFiles, &s.SkippedByReason, &s.Truncated, &s.AddedFiles, &s.ModifiedFiles,
		&s.DeletedFiles, &s.UnchangedFiles, &s.BlobsRead, &s.ErrorMessage, &s.ScannedAt, &s.PublishedAt,
		&s.SupersededAt, &s.CreatedAt, &s.UpdatedAt}
}

func fileDestinations(f *domain.SourceFile) []any {
	return []any{&f.ID, &f.SnapshotID, &f.RepositoryID, &f.Path, &f.BlobOID, &f.Mode, &f.SizeBytes,
		&f.SHA256, &f.LineCount, &f.IsExecutable, &f.CreatedAt}
}

func scanSnapshot(row pgx.Row) (domain.Snapshot, error) {
	var snapshot domain.Snapshot
	err := row.Scan(snapshotDestinations(&snapshot)...)
	return snapshot, repositoryError(err)
}

func (s *PostgresSnapshotStore) GetActiveSnapshot(ctx context.Context, repoID uuid.UUID) (domain.Snapshot, error) {
	return scanSnapshot(db.ConnFromContext(ctx, s.pool).QueryRow(ctx, "SELECT "+snapshotColumns+
		" FROM snapshots WHERE repository_id = $1 AND status = 'active'", repoID))
}

func (s *PostgresSnapshotStore) ListFileMetadataBySnapshot(ctx context.Context, snapshotID uuid.UUID) ([]domain.SourceFile, error) {
	rows, err := db.ConnFromContext(ctx, s.pool).Query(ctx,
		"SELECT "+fileColumns+" FROM source_files WHERE snapshot_id = $1 ORDER BY path", snapshotID)
	if err != nil {
		return nil, repositoryError(err)
	}
	files, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.SourceFile, error) {
		var file domain.SourceFile
		err := row.Scan(fileDestinations(&file)...)
		return file, err
	})
	return files, repositoryError(err)
}

func (s *PostgresSnapshotStore) TouchRepositorySyncedAt(ctx context.Context, projectID, repoID uuid.UUID) error {
	tag, err := db.ConnFromContext(ctx, s.pool).Exec(ctx,
		"UPDATE repositories SET last_synced_at = NOW(), updated_at = NOW() WHERE project_id = $1 AND id = $2",
		projectID, repoID)
	if err != nil {
		return repositoryError(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrNotFound
	}
	return nil
}

type ReusedSnapshotFile struct {
	SourcePath string
	Path       string
	Mode       string
}

type PublishSnapshotRequest struct {
	ProjectID                uuid.UUID
	ExpectedParentSnapshotID *uuid.UUID
	Snapshot                 domain.Snapshot
	ReusedFiles              []ReusedSnapshotFile
	NewFiles                 []domain.SourceFile
	BeforePromoteHook        func(context.Context, uuid.UUID) error
}

func (s *PostgresSnapshotStore) PublishSnapshot(ctx context.Context, txManager db.TxManager, req PublishSnapshotRequest) (domain.Snapshot, error) {
	snapshot := req.Snapshot
	repoID := snapshot.RepositoryID
	snapshot.ID = uuid.New()
	if snapshot.CommitParents == nil {
		snapshot.CommitParents = []string{}
	}
	if snapshot.SkippedByReason == nil {
		snapshot.SkippedByReason = map[string]int{}
	}
	var published domain.Snapshot
	err := txManager.WithinTx(ctx, func(ctx context.Context) error {
		conn, ok := db.ConnFromContext(ctx, s.pool).(pgx.Tx)
		if !ok {
			return errors.New("snapshot publishing requires a PostgreSQL transaction")
		}
		var lockedID uuid.UUID
		if err := conn.QueryRow(ctx, "SELECT id FROM repositories WHERE project_id = $1 AND id = $2 FOR UPDATE",
			req.ProjectID, repoID).Scan(&lockedID); err != nil {
			return repositoryError(err)
		}
		var activeID uuid.UUID
		err := conn.QueryRow(ctx, "SELECT id FROM snapshots WHERE repository_id = $1 AND status = 'active'", repoID).Scan(&activeID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return repositoryError(err)
		}
		activeFound := err == nil
		parent := req.ExpectedParentSnapshotID
		if (parent == nil && activeFound) || (parent != nil && (!activeFound || activeID != *parent)) {
			return fmt.Errorf("%w: active snapshot changed before publish", domain.ErrConflict)
		}
		if err := conn.QueryRow(ctx, "SELECT COALESCE(MAX(version), 0) + 1 FROM snapshots WHERE repository_id = $1",
			repoID).Scan(&snapshot.Version); err != nil {
			return repositoryError(err)
		}
		if err := insertStagingSnapshot(ctx, conn, snapshot, req.ExpectedParentSnapshotID); err != nil {
			return err
		}
		if len(req.ReusedFiles) > 0 {
			if err := copyReusedFiles(ctx, conn, snapshot, req.ExpectedParentSnapshotID, req.ReusedFiles); err != nil {
				return err
			}
		}
		if len(req.NewFiles) > 0 {
			if err := insertSnapshotFiles(ctx, conn, snapshot, req.NewFiles); err != nil {
				return err
			}
		}
		if req.BeforePromoteHook != nil {
			if err := req.BeforePromoteHook(ctx, snapshot.ID); err != nil {
				return err
			}
		}
		if activeFound {
			_, err := conn.Exec(ctx, `UPDATE snapshots SET status = 'superseded', superseded_at = NOW(), updated_at = NOW()
 WHERE id = $1`, activeID)
			if err != nil {
				return repositoryError(err)
			}
		}
		published, err = scanSnapshot(conn.QueryRow(ctx, `UPDATE snapshots
 SET status = 'active', published_at = NOW(), updated_at = NOW() WHERE id = $1 RETURNING `+snapshotColumns, snapshot.ID))
		if err != nil {
			return err
		}
		return s.TouchRepositorySyncedAt(ctx, req.ProjectID, repoID)
	})
	if err != nil {
		return domain.Snapshot{}, err
	}
	return published, nil
}

func insertStagingSnapshot(ctx context.Context, conn db.DBTX, s domain.Snapshot, parentID *uuid.UUID) error {
	_, err := conn.Exec(ctx, `INSERT INTO snapshots (
 id, repository_id, job_id, parent_snapshot_id, version, status,
 resolved_ref, commit_hash, tree_hash, scanner_config_hash, commit_author_name, commit_author_email,
 commit_authored_at, commit_committer_name, commit_committer_email, commit_committed_at, commit_subject, commit_parents,
 total_tree_entries, included_files, included_bytes, skipped_files, skipped_by_reason, truncated,
 added_files, modified_files, deleted_files, unchanged_files, blobs_read, scanned_at)
 VALUES ($1,$2,$3,$4,$5,'staging',$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,
 $18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29)`,
		s.ID, s.RepositoryID, s.JobID, parentID, s.Version,
		s.ResolvedRef, s.CommitHash, s.TreeHash, s.ScannerConfigHash, s.CommitAuthorName, s.CommitAuthorEmail,
		s.CommitAuthoredAt, s.CommitCommitterName, s.CommitCommitterEmail, s.CommitCommittedAt, s.CommitSubject, s.CommitParents,
		s.TotalTreeEntries, s.IncludedFiles, s.IncludedBytes, s.SkippedFiles, s.SkippedByReason, s.Truncated,
		s.AddedFiles, s.ModifiedFiles, s.DeletedFiles, s.UnchangedFiles, s.BlobsRead, s.ScannedAt)
	return repositoryError(err)
}

func copyReusedFiles(ctx context.Context, conn db.DBTX, s domain.Snapshot, parentID *uuid.UUID, reused []ReusedSnapshotFile) error {
	ids := make([]uuid.UUID, len(reused))
	sourcePaths, paths, modes := make([]string, len(reused)), make([]string, len(reused)), make([]string, len(reused))
	for i, file := range reused {
		ids[i], sourcePaths[i], paths[i], modes[i] = uuid.New(), file.SourcePath, file.Path, file.Mode
	}
	tag, err := conn.Exec(ctx, `INSERT INTO source_files
 (id, snapshot_id, repository_id, path, blob_oid, mode, size_bytes, sha256, line_count, is_executable, content)
 SELECT u.id, $1, $2, u.path, parent.blob_oid, u.mode, parent.size_bytes, parent.sha256,
        parent.line_count, u.mode = '100755', parent.content
 FROM unnest($4::uuid[], $5::text[], $6::text[], $7::text[]) AS u(id, source_path, path, mode)
 JOIN source_files parent ON parent.snapshot_id = $3 AND parent.path = u.source_path
                         AND parent.repository_id = $2`,
		s.ID, s.RepositoryID, parentID, ids, sourcePaths, paths, modes)
	if err != nil {
		return repositoryError(err)
	}
	if tag.RowsAffected() != int64(len(reused)) {
		return fmt.Errorf("%w: copied %d of %d files from parent snapshot", domain.ErrConflict, tag.RowsAffected(), len(reused))
	}
	return nil
}

func insertSnapshotFiles(ctx context.Context, tx pgx.Tx, s domain.Snapshot, files []domain.SourceFile) error {
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"source_files"},
		[]string{"id", "snapshot_id", "repository_id", "path", "blob_oid", "mode", "size_bytes",
			"sha256", "line_count", "is_executable", "content"},
		pgx.CopyFromSlice(len(files), func(i int) ([]any, error) {
			f := files[i]
			var content string
			if f.Content != nil {
				content = *f.Content
			}
			return []any{uuid.New(), s.ID, s.RepositoryID, f.Path, f.BlobOID, f.Mode, f.SizeBytes,
				f.SHA256, f.LineCount, f.IsExecutable, content}, nil
		}))
	return repositoryError(err)
}

func (s *PostgresSnapshotStore) PruneSnapshots(ctx context.Context, repoID uuid.UUID, retainSuperseded int, failedBefore time.Time) (int64, error) {
	tag, err := db.ConnFromContext(ctx, s.pool).Exec(ctx, `DELETE FROM snapshots WHERE repository_id = $1 AND (
 (status = 'superseded' AND id IN (
   SELECT id FROM snapshots WHERE repository_id = $1 AND status = 'superseded' ORDER BY version DESC OFFSET $2
 )) OR (status IN ('failed', 'staging') AND created_at < $3)
 )`, repoID, int64(retainSuperseded), failedBefore)
	if err != nil {
		return 0, repositoryError(err)
	}
	return tag.RowsAffected(), nil
}

func (s *PostgresSnapshotStore) ListSnapshots(ctx context.Context, projectID, repoID uuid.UUID, filter domain.ListSnapshotsFilter) (domain.SnapshotListResult, error) {
	result := domain.SnapshotListResult{Items: []domain.Snapshot{}, Limit: filter.Limit, Offset: filter.Offset}
	rows, err := db.ConnFromContext(ctx, s.pool).Query(ctx, `
 WITH scoped AS (
  SELECT id FROM repositories WHERE project_id = $1 AND id = $2
 ), matching AS NOT MATERIALIZED (
  SELECT `+snapshotColumns+` FROM snapshots WHERE repository_id IN (SELECT id FROM scoped)
 ), page AS (
  SELECT * FROM matching ORDER BY version DESC LIMIT $3 OFFSET $4
 )
 SELECT page.*, totals.total FROM scoped
 CROSS JOIN (SELECT count(*) AS total FROM matching) totals
 LEFT JOIN page ON true ORDER BY page.version DESC`, projectID, repoID, int64(filter.Limit), int64(filter.Offset))
	if err != nil {
		return result, repositoryError(err)
	}
	result.Items, err = collectSnapshotPage(rows, &result.Total, snapshotDestinations)
	return result, err
}

func (s *PostgresSnapshotStore) GetActiveSnapshotByProject(ctx context.Context, projectID, repoID uuid.UUID) (domain.Snapshot, error) {
	return scanSnapshot(db.ConnFromContext(ctx, s.pool).QueryRow(ctx, "SELECT "+snapshotColumns+` FROM snapshots
 WHERE repository_id = (SELECT id FROM repositories WHERE project_id = $1 AND id = $2) AND status = 'active'`, projectID, repoID))
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)

func (s *PostgresSnapshotStore) SearchActiveFiles(ctx context.Context, projectID, repoID uuid.UUID, filter domain.SearchFilesFilter) (domain.SourceFileListResult, error) {
	result := domain.SourceFileListResult{Items: []domain.SourceFile{}, Limit: filter.Limit, Offset: filter.Offset}
	query := "%" + likeEscaper.Replace(filter.Query) + "%"
	rows, err := db.ConnFromContext(ctx, s.pool).Query(ctx, `
 WITH scoped AS (
  SELECT id FROM repositories WHERE project_id = $1 AND id = $2
 ), active AS (
  SELECT id AS snapshot_id FROM snapshots WHERE repository_id IN (SELECT id FROM scoped) AND status = 'active'
 ), matching AS NOT MATERIALIZED (
  SELECT `+fileColumns+` FROM source_files JOIN active USING (snapshot_id)
  WHERE repository_id IN (SELECT id FROM scoped)
    AND ($3 = '%%' OR path ILIKE $3 OR content ILIKE $3)
    AND ($4 = '%' OR path LIKE $4)
 ), page AS (
  SELECT * FROM matching ORDER BY path COLLATE "C" LIMIT $5 OFFSET $6
 )
 SELECT page.*, totals.total FROM scoped
 CROSS JOIN (SELECT count(*) AS total FROM matching) totals
 LEFT JOIN page ON true ORDER BY page.path COLLATE "C"`, projectID, repoID,
		query, likeEscaper.Replace(filter.PathPrefix)+"%", int64(filter.Limit), int64(filter.Offset))
	if err != nil {
		return result, repositoryError(err)
	}
	result.Items, err = collectSnapshotPage(rows, &result.Total, fileDestinations)
	return result, err
}

func collectSnapshotPage[T any](rows pgx.Rows, total *int64, destinations func(*T) []any) ([]T, error) {
	defer rows.Close()
	items := []T{}
	found := false
	for rows.Next() {
		found = true
		if rows.RawValues()[0] == nil {
			// A count-only row keeps the total available beyond the last page.
			slots := make([]any, len(rows.RawValues()))
			slots[len(slots)-1] = total
			if err := rows.Scan(slots...); err != nil {
				return nil, repositoryError(err)
			}
			continue
		}
		var item T
		if err := rows.Scan(append(destinations(&item), total)...); err != nil {
			return nil, repositoryError(err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, repositoryError(err)
	}
	if !found {
		return nil, domain.ErrNotFound
	}
	return items, nil
}

func (s *PostgresSnapshotStore) GetActiveFile(ctx context.Context, projectID, repoID uuid.UUID, filePath string) (domain.SourceFile, error) {
	var file domain.SourceFile
	err := db.ConnFromContext(ctx, s.pool).QueryRow(ctx, "SELECT "+fileColumns+`, content FROM source_files
 WHERE repository_id = $2 AND path = $3 AND snapshot_id = (
  SELECT id FROM snapshots WHERE status = 'active'
  AND repository_id = (SELECT id FROM repositories WHERE project_id = $1 AND id = $2)
 )`, projectID, repoID, filePath).Scan(append(fileDestinations(&file), &file.Content)...)
	return file, repositoryError(err)
}
