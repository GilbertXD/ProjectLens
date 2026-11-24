package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"projectlens/internal/config"
	"projectlens/internal/db"
	"projectlens/internal/domain"
	"projectlens/internal/repository"
)

type Scheduler struct {
	Repositories *repository.PostgresRepositoryStore
	Tx           db.TxManager
	Jobs         *JobService
	Config       config.ScheduleConfig
}

func (s *Scheduler) Policy(ctx context.Context, t domain.SyncTarget, kind string, in *domain.PolicyInput) ([]domain.Policy, error) {
	repo, err := s.Repositories.GetByID(ctx, t.ProjectID, t.RepositoryID)
	if err != nil {
		return nil, err
	}
	if in != nil {
		if domain.SyncJobType(kind) == "" || kind == "github_activity" && repo.SourceType != domain.RepositorySourceGitHub || in.Enabled == nil || in.IntervalSeconds < s.Config.MinIntervalSeconds || in.IntervalSeconds > 31536000 {
			return nil, fmt.Errorf("%w: invalid kind, enabled flag or interval", domain.ErrInvalidInput)
		}
		if err := s.Repositories.SetPolicy(ctx, t.RepositoryID, kind, *in); err != nil {
			return nil, err
		}
	}
	return s.Repositories.Policies(ctx, t.RepositoryID)
}
func (s *Scheduler) Tick(ctx context.Context) error {
	return s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		due, err := s.Repositories.DuePolicies(ctx, s.Config.Batch)
		if err != nil {
			return err
		}
		for _, p := range due {
			if p.Kind == "github_activity" {
				until, err := s.Repositories.ActivityBackoff(ctx, p.Repo)
				if err != nil {
					return err
				}
				if until != nil {
					if err := s.Repositories.NextPolicy(ctx, p, nil, until, s.Config.MinIntervalSeconds); err != nil {
						return err
					}
					continue
				}
			}
			payload, _ := json.Marshal(domain.SyncTarget{ProjectID: p.Project, RepositoryID: p.Repo, Ref: "HEAD"})
			job, _, err := s.Jobs.Enqueue(ctx, domain.CreateJobInput{Type: domain.SyncJobType(p.Kind), Payload: payload})
			if errors.Is(err, domain.ErrNotFound) {
				continue
			}
			if errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrInvalidInput) {
				if err := s.Repositories.NextPolicy(ctx, p, nil, nil, s.Config.MinIntervalSeconds); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			if err := s.Repositories.NextPolicy(ctx, p, &job.ID, nil, s.Config.MinIntervalSeconds); err != nil {
				return err
			}
		}
		return nil
	})
}
func (s *Scheduler) Run(ctx context.Context, log *slog.Logger, work func(context.Context)) {
	done := make(chan struct{})
	loop, cancel := context.WithCancel(ctx)
	go func() {
		defer close(done)
		timer := time.NewTicker(5 * time.Second)
		defer timer.Stop()
		for loop.Err() == nil {
			if err := s.Tick(loop); err != nil && loop.Err() == nil {
				log.Error("schedule sync jobs", "error", err)
			}
			select {
			case <-loop.Done():
				return
			case <-timer.C:
			}
		}
	}()
	work(ctx)
	cancel()
	<-done
}
