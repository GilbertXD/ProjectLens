package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"projectlens/internal/config"
)

func TestHealthHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	app := config.AppConfig{Name: "ProjectLens", Env: "test", Version: "0.1.0"}
	engine := gin.New()
	engine.GET("/healthz", HealthHandler(app))

	start := time.Now().Truncate(time.Second)
	resp := httptest.NewRecorder()
	engine.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if resp.Code != http.StatusOK || !strings.HasPrefix(resp.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("status = %d, headers = %v, body = %s", resp.Code, resp.Header(), resp.Body)
	}
	var body map[string]string
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" || body["service"] != app.Name || body["env"] != app.Env || body["version"] != app.Version {
		t.Fatalf("unexpected metadata: %v", body)
	}
	timestamp, err := time.Parse(time.RFC3339, body["timestamp"])
	if err != nil || !strings.HasSuffix(body["timestamp"], "Z") || timestamp.Before(start) || timestamp.After(time.Now()) {
		t.Fatalf("timestamp = %q, want current UTC RFC3339 timestamp", body["timestamp"])
	}
	if uptime, err := time.ParseDuration(body["uptime"]); err != nil || uptime < 0 {
		t.Fatalf("uptime = %q, want non-negative duration", body["uptime"])
	}
}
