package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"projectlens/internal/config"
	"projectlens/internal/db"
	"projectlens/internal/github"
	"projectlens/internal/gitrepo"
	"projectlens/internal/gitscan"
	"projectlens/internal/handler"
	"projectlens/internal/logger"
	"projectlens/internal/repository"
	"projectlens/internal/router"
	"projectlens/internal/server"
	"projectlens/internal/service"
	"projectlens/internal/worker"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("server", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", os.Getenv("APP_CONFIG_FILE"), "path to the YAML configuration file")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected argument %q\n", flags.Arg(0))
		return 2
	}

	if *configPath == "" {
		const defaultPath = "configs/config.yaml"
		if _, err := os.Stat(defaultPath); err == nil {
			*configPath = defaultPath
		} else if !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(stderr, "check config file %q: %v\n", defaultPath, err)
			return 1
		}
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "load configuration: %v\n", err)
		return 1
	}
	log := logger.New(cfg.Log, stdout)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := db.NewPool(ctx, cfg.Database)
	if err != nil {
		fmt.Fprintf(stderr, "initialize database: %v\n", err)
		return 1
	}
	defer pool.Close()
	if cfg.Database.AutoMigrate {
		if err := db.MigrateUp(ctx, pool); err != nil {
			fmt.Fprintf(stderr, "migrate database: %v\n", err)
			return 1
		}
	}
	projectStore := repository.NewPostgresProjectRepository(pool)
	projects := service.NewProjectService(projectStore, db.NewPgxTxManager(pool))
	repositoryStore := repository.NewPostgresRepositoryStore(pool)
	repositories := service.NewRepositoryService(projectStore, repositoryStore,
		gitrepo.NewLocalInspector(cfg.Repository.AllowedLocalRoots), github.NewClient(cfg.GitHub, nil))
	registry := &worker.Registry{}
	if err := worker.RegisterMockHandlers(registry); err != nil {
		fmt.Fprintf(stderr, "register job handlers: %v\n", err)
		return 1
	}
	scanner := gitscan.NewScanner(cfg.Scanner, cfg.Repository, cfg.GitHub)
	if err := worker.RegisterScanHandler(registry, repositoryStore, scanner); err != nil {
		fmt.Fprintf(stderr, "register repository scan handler: %v\n", err)
		return 1
	}
	snapshotStore, err := repository.NewPostgresSnapshotStore(ctx, pool)
	if err != nil {
		fmt.Fprintf(stderr, "initialize snapshot store: %v\n", err)
		return 1
	}
	defer snapshotStore.Close()
	syncer := service.NewSyncService(repositoryStore, snapshotStore, db.NewPgxTxManager(pool), scanner, cfg.Sync, log)
	if err := worker.RegisterSyncHandler(registry, syncer); err != nil {
		fmt.Fprintf(stderr, "register repository sync handler: %v\n", err)
		return 1
	}
	jobStore := repository.NewPostgresJobStore(pool)
	jobs := service.NewJobService(jobStore, registry, cfg.Worker)
	routes := router.New(cfg, log, router.Dependencies{
		Projects:     handler.NewProjectHandler(projects, log),
		Repositories: handler.NewRepositoryHandler(repositories, log),
		Snapshots:    handler.NewSnapshotHandler(syncer, log),
		Pinger:       pool,
		Jobs:         handler.NewJobHandler(jobs, log),
	})
	runner := worker.NewRunner(jobStore, registry, cfg.Worker, log)
	if err := runHTTPAndWorker(ctx, cfg.Worker.Mode, server.New(cfg, log, routes).Run, runner.Run); err != nil {
		fmt.Fprintf(stderr, "run server: %v\n", err)
		return 1
	}
	return 0
}

// Stop the worker when HTTP exits, including listener startup failures.
func runHTTPAndWorker(ctx context.Context, mode string, serve func(context.Context) error, work func(context.Context)) error {
	if mode != "embedded" {
		return serve(ctx)
	}
	ctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		work(ctx)
	}()
	err := serve(ctx)
	stop()
	<-done
	return err
}
