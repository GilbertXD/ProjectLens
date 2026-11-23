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
	"projectlens/internal/gitscan"
	"projectlens/internal/logger"
	"projectlens/internal/repository"
	"projectlens/internal/worker"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("worker", flag.ContinueOnError)
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
	repositoryStore := repository.NewPostgresRepositoryStore(pool)
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
	worker.NewRunner(repository.NewPostgresJobStore(pool), registry, cfg.Worker, log).Run(ctx)
	return 0
}
