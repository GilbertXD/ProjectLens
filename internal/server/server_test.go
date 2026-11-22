package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"projectlens/internal/config"
	"projectlens/internal/middleware"
	"projectlens/internal/router"
)

func TestServeAndGracefulShutdown(t *testing.T) {
	const timeout = 5 * time.Second
	cfg := config.Config{
		App: config.AppConfig{Name: "ProjectLens", Env: "test", Version: "0.1.0"},
		Server: config.ServerConfig{
			Host: "127.0.0.1", Port: 0,
			ReadTimeout: timeout, WriteTimeout: timeout, IdleTimeout: timeout, ShutdownTimeout: timeout,
		},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine := router.New(cfg, log)
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseRequest) })
	defer release()
	engine.GET("/in-flight", func(c *gin.Context) {
		close(requestStarted)
		<-releaseRequest
		c.Status(http.StatusNoContent)
	})

	srv := New(cfg, log, engine)
	shutdownStarted := make(chan struct{})
	srv.http.RegisterOnShutdown(func() { close(shutdownStarted) })
	ln, err := net.Listen("tcp", srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.run(ctx, ln) }()

	client := &http.Client{Timeout: timeout}
	defer client.CloseIdleConnections()
	baseURL := "http://" + ln.Addr().String()
	resp, err := client.Get(baseURL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]string
	err = json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get(middleware.RequestIDHeader) == "" ||
		body["status"] != "ok" || body["service"] != cfg.App.Name || body["env"] != cfg.App.Env ||
		body["version"] != cfg.App.Version {
		t.Fatalf("status = %d, headers = %v, body = %v", resp.StatusCode, resp.Header, body)
	}

	inFlightDone := make(chan struct{})
	var inFlightStatus int
	var inFlightErr error
	go func() {
		defer close(inFlightDone)
		response, err := client.Get(baseURL + "/in-flight")
		inFlightErr = err
		if err == nil {
			defer response.Body.Close()
			inFlightStatus = response.StatusCode
		}
	}()
	select {
	case <-requestStarted:
	case <-time.After(timeout):
		t.Fatal("in-flight request did not start")
	}
	cancel()
	select {
	case <-shutdownStarted:
	case <-time.After(timeout):
		t.Fatal("shutdown did not start")
	}
	select {
	case err := <-done:
		t.Fatalf("server stopped before the request completed: %v", err)
	default:
	}
	release()
	select {
	case <-inFlightDone:
		if inFlightErr != nil || inFlightStatus != http.StatusNoContent {
			t.Fatalf("in-flight request status = %d, error = %v", inFlightStatus, inFlightErr)
		}
	case <-time.After(timeout):
		t.Fatal("in-flight request did not finish")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("graceful shutdown: %v", err)
		}
	case <-time.After(timeout):
		t.Fatal("server did not shut down")
	}
	client.CloseIdleConnections()
	if resp, err := client.Get(baseURL + "/healthz"); err == nil {
		resp.Body.Close()
		t.Fatal("server still accepts connections after shutdown")
	}
}
