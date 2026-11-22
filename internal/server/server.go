package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"projectlens/internal/config"
)

type Server struct {
	http            *http.Server
	log             *slog.Logger
	shutdownTimeout time.Duration
}

func New(cfg config.Config, log *slog.Logger, engine http.Handler) *Server {
	return &Server{
		http: &http.Server{
			Addr:         net.JoinHostPort(cfg.Server.Host, strconv.Itoa(int(cfg.Server.Port))),
			Handler:      engine,
			ReadTimeout:  cfg.Server.ReadTimeout,
			WriteTimeout: cfg.Server.WriteTimeout,
			IdleTimeout:  cfg.Server.IdleTimeout,
			ErrorLog:     slog.NewLogLogger(log.Handler(), slog.LevelError),
		},
		log:             log,
		shutdownTimeout: cfg.Server.ShutdownTimeout,
	}
}

func (s *Server) Addr() string {
	return s.http.Addr
}

// Start serves HTTP on ln until the listener or server is closed.
func (s *Server) Start(ln net.Listener) error {
	s.log.Info("http server listening", "addr", ln.Addr().String())
	if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve HTTP on %s: %w", ln.Addr(), err)
	}
	return nil
}

// Run listens on the configured address and shuts down when ctx is canceled.
func (s *Server) Run(ctx context.Context) error {
	var listen net.ListenConfig
	ln, err := listen.Listen(ctx, "tcp", s.Addr())
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.Addr(), err)
	}
	return s.run(ctx, ln)
}

func (s *Server) run(ctx context.Context, ln net.Listener) error {
	done := make(chan error, 1)
	go func() {
		done <- s.Start(ln)
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		s.log.Info("http server shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
		defer cancel()
		if err := s.http.Shutdown(shutdownCtx); err != nil {
			return errors.Join(fmt.Errorf("shut down HTTP server: %w", err), s.http.Close(), <-done)
		}
		return <-done
	}
}
