export type ProjectStatus = 'active' | 'archived'
export type RepositorySourceType = 'local_git' | 'github'
export type RepositoryRole = 'primary' | 'reference'
export type KnowledgeScope = 'project' | 'repository'
export type SnapshotStatus = 'staging' | 'active' | 'superseded' | 'failed'
export type JobStatus = 'pending' | 'running' | 'succeeded' | 'failed' | 'cancelled'
export type SearchScope = 'project' | 'repository' | 'cross_repository' | 'all'
export type SearchSourceType = RepositorySourceType | 'document'
export type SearchCacheStatus = 'hit' | 'miss' | 'bypass' | 'degraded'

interface Timestamps {
  created_at: string
  updated_at: string
}

export interface Project extends Timestamps {
  id: string
  slug: string
  name: string
  description: string
  status: ProjectStatus
}

export interface Repository extends Timestamps {
  id: string
  project_id: string
  source_type: RepositorySourceType
  role: RepositoryRole
  name: string
  source: string
  canonical_source: string
  default_branch: string
  description: string
  remote_url?: string
  local_path?: string
  github_owner?: string
  github_repo?: string
  github_repo_id?: number
  github_visibility?: string
  github_stars?: number
  github_language?: string
  last_synced_at?: string
}

export interface Snapshot extends Timestamps {
  id: string
  repository_id: string
  job_id?: string
  parent_snapshot_id?: string
  version: number
  status: SnapshotStatus
  resolved_ref: string
  commit_hash: string
  tree_hash: string
  scanner_config_hash: string
  commit_author_name: string
  commit_author_email: string
  commit_authored_at: string
  commit_committer_name: string
  commit_committer_email: string
  commit_committed_at: string
  commit_subject: string
  commit_parents: string[]
  total_tree_entries: number
  included_files: number
  included_bytes: number
  skipped_files: number
  skipped_by_reason: Record<string, number>
  truncated: boolean
  added_files: number
  modified_files: number
  deleted_files: number
  unchanged_files: number
  blobs_read: number
  error_message?: string
  scanned_at: string
  published_at?: string
  superseded_at?: string
}

export interface SourceFile {
  id: string
  snapshot_id: string
  repository_id: string
  path: string
  blob_oid: string
  mode: string
  size_bytes: number
  sha256: string
  line_count: number
  is_executable: boolean
  content?: string
  created_at: string
}

export interface CommitHistoryEntry {
  hash: string
  tree_hash: string
  subject: string
  message: string
  author_name: string
  author_email: string
  committer_name: string
  committer_email: string
  html_url?: string
  parents: string[]
  authored_at: string
  committed_at: string
  truncated: boolean
}

export interface CommitHistoryPage {
  window_limit: number
  page_size: number
  complete: boolean
  truncated: boolean
  window_limit_reached: boolean
  window_status: 'complete' | 'partial' | 'window_limit_reached'
  ref: string
  tip: string
  synced_at: string
  total: number
  has_more: boolean
  items: CommitHistoryEntry[]
}

export type FileHistoryEntry = Omit<CommitHistoryEntry, 'message' | 'truncated'> & {
  change_type: string
  path: string
  old_path?: string
}

export interface FileChange {
  path: string
  type: string
  old_path?: string
  binary: boolean
  insertions: number
  deletions: number
}

export interface CommitComparison {
  items: FileChange[]
  total: number
  truncated: boolean
  base: string
  head: string
  patch: string
  patch_truncated: boolean
}

export interface BlameLine {
  line: number
  original_line: number
  commit_hash: string
  author_name: string
  author_email: string
  authored_at: string
  committer_name: string
  committer_email: string
  committed_at: string
  summary: string
  filename: string
  boundary: boolean
  content: string
}

export interface GitRef {
  type: 'branch' | 'tag'
  name: string
  ref: string
  object_hash: string
  commit_hash?: string
}

export interface DocumentOrigin {
  readonly repository_id: string
  readonly repository_name: string
}

export interface DocumentSummary extends Timestamps {
  id: string
  project_id: string
  scope: KnowledgeScope
  repository_id?: string
  readonly origin?: DocumentOrigin
  title: string
  content_bytes: number
  version: number
}

export interface Document extends DocumentSummary {
  content: string
}

export interface SearchLocation {
  scope?: KnowledgeScope
  repository_role?: RepositoryRole
  repository_id?: string
  repository_name?: string
  repository_source?: string
  snapshot_id?: string
  snapshot_version?: number
  commit_hash?: string
  path?: string
  url?: string
  document_id?: string
  document_title?: string
  document_version?: number
  line?: number
  api_path: string
}

export interface SearchHit {
  kind: 'source_file' | 'document'
  id: string
  source_type: SearchSourceType
  project_id: string
  title: string
  snippet: string
  score: number
  updated_at: string
  location: SearchLocation
}

export interface SearchResult {
  query: string
  items: SearchHit[]
  total: number
  limit: number
  offset: number
  search_version: number
  cache_status?: SearchCacheStatus
}

export interface GitHubActivity extends Timestamps {
  repository_id: string
  kind: string
  number?: number
  id?: number
  user?: { login: string }
  title?: string
  body?: string
  state?: string
  html_url?: string
  sha?: string
  path?: string
  commit_id?: string
  line?: number
  truncated?: boolean
}

export interface SyncPolicy {
  kind: 'files' | 'commits' | 'github_activity'
  enabled: boolean
  interval_seconds: number
  next_at?: string | null
  last_job_id?: string | null
  last_success_at?: string | null
  last_error_code?: string | null
}

export interface Job extends Timestamps {
  id: string
  type: string
  status: JobStatus
  payload: Record<string, unknown>
  result?: unknown
  idempotency_key?: string
  attempt: number
  max_attempts: number
  timeout_seconds: number
  run_at: string
  locked_by?: string
  lease_until?: string
  cancel_requested_at?: string
  last_error?: string
  last_error_code?: string
  started_at?: string
  finished_at?: string
}

export interface Health {
  status: string
  service: string
  env: string
  version: string
  timestamp: string
  uptime: string
}

export interface Readiness {
  status: 'ok' | 'unavailable'
  checks: {
    postgres: 'ok' | 'unavailable'
    redis: 'ok' | 'degraded' | 'disabled'
  }
}
