package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"projectlens/internal/config"
	"projectlens/internal/domain"
	"projectlens/internal/handler"
	"projectlens/internal/repository"
	"projectlens/internal/router"
	"projectlens/internal/service"
	"projectlens/internal/worker"
)

func TestScopedRepositorySyncLocks(t *testing.T) {
	f := localSyncFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for _, scope := range []string{"github_activity", "files", "commits", ""} {
		release, err := f.store.AcquireScopedSyncLock(ctx, scope, f.repoID)
		if err != nil {
			t.Fatalf("distinct scope %q blocked: %v", scope, err)
		}
		defer release()
	}
	for _, scope := range []string{"github_activity", "files", "commits", ""} {
		waitCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		acquire := func() (func(), error) {
			if scope == "" {
				return f.store.AcquireRepoSyncLock(waitCtx, f.repoID)
			}
			return f.store.AcquireScopedSyncLock(waitCtx, scope, f.repoID)
		}
		release, err := acquire()
		cancel()
		if release != nil {
			release()
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("scope %q did not serialize: %v", scope, err)
		}
	}
}

func TestScheduleSkipsInvalidAndLockedRepositories(t *testing.T) {
	f := localSyncFixture(t)
	ctx := t.Context()
	reg := &worker.Registry{}
	if err := worker.RegisterSyncHandler(reg, f.svc); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register("repository.github_activity.sync", func(context.Context, domain.Job) (json.RawMessage, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	jobs := service.NewJobService(repository.NewPostgresJobStore(f.pool), reg, f.cfg.Worker)
	s := &service.Scheduler{Repositories: f.repos, Tx: f.tx, Jobs: jobs, Config: config.ScheduleConfig{MinIntervalSeconds: 60, Batch: 4}}
	engine := router.New(f.cfg, f.log, router.Dependencies{Schedule: &handler.ScheduleHandler{Service: s, Log: f.log}})
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, f.apiPath()+"/sync-policy", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != `{"items":[]}` {
		t.Fatalf("empty policy response = %d %s", rec.Code, rec.Body.String())
	}

	local := f.repo(t)
	blocked := local
	blocked.ID, blocked.CanonicalSource = uuid.New(), t.TempDir()
	if _, err := f.repos.Create(ctx, blocked); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO sync_policies(repository_id,kind,enabled,interval_seconds,next_at)
		VALUES($1,'github_activity',true,90,now()-interval '2 minutes'),
		      ($2,'files',true,90,now()-interval '1 minute'),
		      ($1,'files',true,90,now())`, local.ID, blocked.ID); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	deletion, err := f.pool.Begin(runCtx)
	if err != nil {
		t.Fatal(err)
	}
	defer deletion.Rollback(ctx)
	// Hold the repository row before the cascading delete reaches its policy row.
	if _, err := deletion.Exec(runCtx, "SELECT id FROM repositories WHERE id=$1 FOR UPDATE", blocked.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(runCtx); err != nil {
		t.Fatal(err)
	}
	if _, err := deletion.Exec(runCtx, "DELETE FROM repositories WHERE id=$1", blocked.ID); err != nil {
		t.Fatal(err)
	}
	if err := deletion.Commit(runCtx); err != nil {
		t.Fatal(err)
	}
	list, err := jobs.List(ctx, domain.ListJobsFilter{})
	if err != nil || list.Total != 1 || list.Items[0].Type != "repository.sync" {
		t.Fatal(list, err)
	}
	var target domain.SyncTarget
	if err := json.Unmarshal(list.Items[0].Payload, &target); err != nil || target.RepositoryID != local.ID {
		t.Fatal(target, err)
	}
	var advanced bool
	if err := f.pool.QueryRow(ctx, "SELECT next_at>now() FROM sync_policies WHERE repository_id=$1 AND kind='github_activity'", local.ID).Scan(&advanced); err != nil || !advanced {
		t.Fatalf("invalid policy was not advanced: advanced=%t, err=%v", advanced, err)
	}
}

func TestDurableSchedule(t *testing.T) {
	f := localSyncFixture(t)
	ctx := t.Context()
	reg := &worker.Registry{}
	if err := worker.RegisterSyncHandler(reg, f.svc); err != nil {
		t.Fatal(err)
	}
	jobs := service.NewJobService(repository.NewPostgresJobStore(f.pool), reg, f.cfg.Worker)
	s := service.Scheduler{Repositories: f.repos, Tx: f.tx, Jobs: jobs, Config: config.ScheduleConfig{MinIntervalSeconds: 60, Batch: 4}}
	target := domain.SyncTarget{ProjectID: f.projectID, RepositoryID: f.repoID}
	enabled := true
	in := domain.PolicyInput{Enabled: &enabled, IntervalSeconds: 90}
	if _, err := s.Policy(ctx, target, "github_activity", &in); err == nil {
		t.Fatal("local activity accepted")
	}
	if _, err := s.Policy(ctx, target, "files", &in); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			instance := s
			if err := instance.Tick(ctx); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	check := func() {
		list, err := jobs.List(ctx, domain.ListJobsFilter{})
		if err != nil || list.Total != 1 {
			t.Fatal(list, err)
		}
	}
	check()
	state, err := s.Policy(ctx, target, "", nil)
	if err != nil || len(state) != 1 || state[0].Next == nil || state[0].Job == nil {
		t.Fatal(state, err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE sync_scheduler_clock SET next_tick=now(); UPDATE sync_policies SET next_at=now()-interval '2 days'"); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	check()
	enabled = false
	if _, err := s.Policy(ctx, target, "files", &in); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Cancel(ctx, *state[0].Job); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE sync_scheduler_clock SET next_tick=now(); UPDATE sync_policies SET next_at=now()"); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	check()

	// Disabled schedules still permit manual sync. Re-enable while a different ref is active.
	target.Ref = "main"
	payload, _ := json.Marshal(target)
	manual, created, err := jobs.Enqueue(ctx, domain.CreateJobInput{Type: "repository.sync", Payload: payload})
	if err != nil || !created {
		t.Fatal(manual, err)
	}
	enabled = true
	if _, err := s.Policy(ctx, target, "files", &in); err != nil {
		t.Fatal(err)
	}
	tickNow := func() {
		t.Helper()
		if _, err := f.pool.Exec(ctx, "UPDATE sync_scheduler_clock SET next_tick=now()"); err != nil {
			t.Fatal(err)
		}
		if err := s.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	tickNow()
	if list, err := jobs.List(ctx, domain.ListJobsFilter{}); err != nil || list.Total != 2 {
		t.Fatal(list, err)
	}
	if err := f.repos.Delete(ctx, f.projectID, f.repoID); err != nil {
		t.Fatal(err)
	}
	tickNow()
	var policies int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM sync_policies").Scan(&policies); err != nil || policies != 0 {
		t.Fatal(policies, err)
	}
}
