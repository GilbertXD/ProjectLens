package integration

import (
	"encoding/json"
	"sync"
	"testing"

	"projectlens/internal/config"
	"projectlens/internal/domain"
	"projectlens/internal/repository"
	"projectlens/internal/service"
	"projectlens/internal/worker"
)

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
