package domain

import "time"

type PolicyInput struct {
	Enabled         *bool `json:"enabled"`
	IntervalSeconds int   `json:"interval_seconds"`
}
type Policy struct {
	Kind            string     `json:"kind"`
	Enabled         bool       `json:"enabled"`
	IntervalSeconds int        `json:"interval_seconds"`
	Next            *time.Time `json:"next_at"`
	Job             *string    `json:"last_job_id"`
	Success         *time.Time `json:"last_success_at"`
	Failure         *string    `json:"last_error_code"`
}

func SyncJobType(kind string) string {
	switch kind {
	case "files":
		return "repository.sync"
	case "commits":
		return "repository.commits.sync"
	case "github_activity":
		return "repository.github_activity.sync"
	default:
		return ""
	}
}
