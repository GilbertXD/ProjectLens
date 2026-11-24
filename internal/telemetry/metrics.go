package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var metrics = prometheus.NewRegistry()

func histogram(name, help string, labels ...string) *prometheus.HistogramVec {
	h := prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: "projectlens", Name: name, Help: help, Buckets: []float64{.001, .005, .025, .1, .5, 1, 5, 30, 120, 600}}, labels)
	metrics.MustRegister(h)
	return h
}
func counter(name, help string) prometheus.Counter {
	c := prometheus.NewCounter(prometheus.CounterOpts{Namespace: "projectlens", Name: name, Help: help})
	metrics.MustRegister(c)
	return c
}

var (
	HTTP     = histogram("http_request_seconds", "HTTP response latency by status class.", "status_class")
	Jobs     = histogram("job_attempt_seconds", "Execution latency per job attempt.", "type", "outcome")
	Search   = histogram("search_seconds", "Search latency by cache outcome.", "cache")
	GitHub   = histogram("github_request_seconds", "GitHub REST request count and latency.", "status_class")
	Wait     = histogram("github_retry_wait_seconds", "Requested GitHub rate-limit delay.")
	Sync     = histogram("snapshot_sync_seconds", "File synchronization latency.")
	Files    = counter("snapshot_files_total", "Files included across successful sync scans.")
	Bytes    = counter("snapshot_bytes_total", "Bytes included across successful sync scans.")
	Retries  = counter("job_retries_total", "Job attempts started after the first attempt.")
	Expiries = counter("job_lease_expiries_total", "Expired leases reclaimed.")
)

func StatusClass(status int) string {
	if status < 100 || status >= 600 {
		return "error"
	}
	return strconv.Itoa(status/100) + "xx"
}
func JobKind(kind string) string {
	switch kind {
	case "repository.sync", "repository.scan", "repository.commits.sync", "repository.github_activity.sync", "mock.echo", "mock.fail", "mock.sleep", "mock.flaky":
		return kind
	default:
		return "other"
	}
}

var (
	poolDesc = prometheus.NewDesc("projectlens_db_connections", "PostgreSQL pool connections.", []string{"state"}, nil)
	jobsDesc = prometheus.NewDesc("projectlens_jobs", "Stored jobs by type and state.", []string{"type", "status"}, nil)
	dbUp     = prometheus.NewDesc("projectlens_db_scrape_up", "Whether job state was successfully collected.", nil, nil)
)

type databaseMetrics struct{ pool *pgxpool.Pool }

func (d databaseMetrics) Describe(ch chan<- *prometheus.Desc) {
	ch <- poolDesc
	ch <- jobsDesc
	ch <- dbUp
}
func (d databaseMetrics) Collect(ch chan<- prometheus.Metric) {
	s := d.pool.Stat()
	for state, n := range map[string]int32{"acquired": s.AcquiredConns(), "idle": s.IdleConns(), "max": s.MaxConns()} {
		ch <- prometheus.MustNewConstMetric(poolDesc, prometheus.GaugeValue, float64(n), state)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rows, err := d.pool.Query(ctx, `SELECT CASE WHEN type IN ('repository.sync','repository.scan','repository.commits.sync','repository.github_activity.sync','mock.echo','mock.sleep','mock.fail','mock.flaky') THEN type ELSE 'other' END,status,count(*) FROM jobs GROUP BY 1,2`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var kind, state string
			var n int64
			if err = rows.Scan(&kind, &state, &n); err != nil {
				break
			}
			ch <- prometheus.MustNewConstMetric(jobsDesc, prometheus.GaugeValue, float64(n), kind, state)
		}
		if err == nil {
			err = rows.Err()
		}
	}
	up := 0.
	if err == nil {
		up = 1
	}
	ch <- prometheus.MustNewConstMetric(dbUp, prometheus.GaugeValue, up)
}
func StartMetrics(addr string, pool *pgxpool.Pool, log *slog.Logger) (func(), error) {
	if addr == "" {
		return func() {}, nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil || !net.ParseIP(strings.Trim(host, "[]")).IsLoopback() {
		return nil, fmt.Errorf("metrics listener requires a loopback IP address")
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	reg := prometheus.NewRegistry()
	reg.MustRegister(databaseMetrics{pool})
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(prometheus.Gatherers{metrics, reg}, promhttp.HandlerOpts{}))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 5 * time.Second}
	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Error("metrics server failed", "error", err)
		}
	}()
	log.Info("metrics listening", "address", listener.Addr().String())
	return func() { _ = srv.Close() }, nil
}
