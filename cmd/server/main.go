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
	"projectlens/internal/logger"
	"projectlens/internal/router"
	"projectlens/internal/server"
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
	if err := server.New(cfg, log, router.New(cfg, log)).Run(ctx); err != nil {
		fmt.Fprintf(stderr, "run server: %v\n", err)
		return 1
	}
	return 0
}
