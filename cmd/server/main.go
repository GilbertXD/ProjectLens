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

	"projectlens/internal/cache"
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
	log.Info("server starting", "env", cfg.App.Env, "version", cfg.App.Version,
		"worker_mode", cfg.Worker.Mode, "database_target", logger.RedactDSN(cfg.Database.DSN),
		"auth_enabled", cfg.Auth.Enabled, "auth_token_count", len(cfg.Auth.BearerTokens),
		"max_in_flight", cfg.Server.MaxInFlight, "max_body_bytes", cfg.Server.MaxBodyBytes, "request_timeout", cfg.Server.RequestTimeout, "redis_enabled", cfg.Redis.Enabled)
	defer log.Info("server stopped")
	if cfg.App.Env == "production" && !cfg.Auth.Enabled {
		log.Warn("API authentication is disabled in production", "auth_enabled", false)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := db.NewPool(ctx, cfg.Database)
	if err != nil {
		log.Error("initialize database failed", "database_target", logger.RedactDSN(cfg.Database.DSN))
		return 1
	}
	defer pool.Close()
	if cfg.Database.AutoMigrate {
		if err := db.MigrateUp(ctx, pool); err != nil {
			log.Error("migrate database failed")
			return 1
		}
	}
	projectStore := repository.NewPostgresProjectRepository(pool)
	projects := service.NewProjectService(projectStore, db.NewPgxTxManager(pool))
	documents := service.NewDocumentService(projectStore, repository.NewPostgresDocumentStore(pool), cfg.Document.MaxContentBytes)
	repositoryStore := repository.NewPostgresRepositoryStore(pool)
	repositories := service.NewRepositoryService(projectStore, repositoryStore,
		gitrepo.NewLocalInspector(cfg.Repository.AllowedLocalRoots), github.NewClient(cfg.GitHub, nil))
	registry := &worker.Registry{}
	if err := worker.RegisterConfiguredMocks(registry, cfg); err != nil {
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
	activity := &service.ActivityService{Repositories: repositoryStore, Locks: snapshotStore, Client: github.NewClient(cfg.GitHub, nil), Tx: db.NewPgxTxManager(pool)}
	if err := worker.RegisterTarget(registry, "repository.github_activity.sync", activity.Sync); err != nil {
		log.Error("register activity handler", "error", err)
		return 1
	}
	history := &service.HistoryService{Repositories: repositoryStore, Locks: snapshotStore, Scanner: scanner, Tx: db.NewPgxTxManager(pool)}
	if err := worker.RegisterTarget(registry, "repository.commits.sync", history.Sync); err != nil {
		log.Error("register history handler", "error", err)
		return 1
	}
	syncer := service.NewSyncService(repositoryStore, snapshotStore, db.NewPgxTxManager(pool), scanner, cfg.Sync, log)
	if err := worker.RegisterSyncHandler(registry, syncer); err != nil {
		fmt.Fprintf(stderr, "register repository sync handler: %v\n", err)
		return 1
	}
	jobStore := repository.NewPostgresJobStore(pool)
	jobs := service.NewJobService(jobStore, registry, cfg.Worker)
	var searchCache service.SearchCache
	var cachePinger handler.CachePinger
	if cfg.Redis.Enabled {
		redisCache := cache.NewRedisSearchCache(cfg.Redis)
		defer redisCache.Close()
		probeCtx, cancel := context.WithTimeout(ctx, cfg.Redis.DialTimeout)
		if err := redisCache.Ping(probeCtx); err != nil {
			log.WarnContext(ctx, "Redis search cache unavailable at startup")
		}
		cancel()
		searchCache = redisCache
		cachePinger = redisCache
	}
	search := service.NewSearchService(repository.NewPostgresSearchStore(pool), searchCache, cfg.Search, cfg.Redis.KeyPrefix, log)
	routes := router.New(cfg, log, router.Dependencies{
		Projects:     handler.NewProjectHandler(projects, log),
		Documents:    handler.NewDocumentHandler(documents, log, cfg.Document.MaxContentBytes),
		Repositories: handler.NewRepositoryHandler(repositories, log),
		Activity:     &handler.ActivityHandler{Store: repositoryStore, Log: log},
		History:      &handler.HistoryHandler{Service: history, Log: log},
		Snapshots:    handler.NewSnapshotHandler(syncer, log),
		Search:       handler.NewSearchHandler(search, log),
		Pinger:       pool,
		CachePinger:  cachePinger,
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
