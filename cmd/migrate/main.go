package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"projectlens/internal/config"
	"projectlens/internal/db"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	path := flag.String("config", "", "path to the YAML configuration file")
	flag.Parse()
	action := flag.Arg(0)
	if flag.NArg() != 1 || (action != "up" && action != "down" && action != "status") {
		return fmt.Errorf("usage: migrate [-config path] up|down|status")
	}
	if *path == "" && os.Getenv("APP_CONFIG_FILE") == "" {
		if _, err := os.Stat("configs/config.yaml"); err == nil {
			*path = "configs/config.yaml"
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := db.NewPool(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()
	switch action {
	case "up":
		return db.MigrateUp(ctx, pool)
	case "down":
		return db.MigrateDown(ctx, pool)
	default:
		statuses, err := db.MigrateStatus(ctx, pool)
		if err != nil {
			return err
		}
		for _, status := range statuses {
			fmt.Printf("%s %s\n", status.State, status.Source.Path)
		}
		return nil
	}
}
