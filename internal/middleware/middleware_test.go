package middleware

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"projectlens/internal/config"
	"projectlens/internal/logger"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

func TestRequestID(t *testing.T) {
	engine := gin.New()
	engine.Use(RequestID())
	engine.GET("/", func(c *gin.Context) { c.String(http.StatusOK, "%s", GetRequestID(c)) })

	for _, incoming := range []string{"client-request-id", "", "with space", "with\r\nnewline", "unicode-☀", strings.Repeat("a", 129), strings.Repeat("z", 128)} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(RequestIDHeader, incoming)
		resp := httptest.NewRecorder()
		engine.ServeHTTP(resp, req)

		id := resp.Header().Get(RequestIDHeader)
		if resp.Code != http.StatusOK || resp.Body.String() != id {
			t.Fatalf("status, body, and request ID = %d, %q, %q", resp.Code, resp.Body.String(), id)
		}
		if incoming == "client-request-id" || incoming == strings.Repeat("z", 128) {
			if id != incoming {
				t.Fatalf("request ID = %q, want %q", id, incoming)
			}
			continue
		}
		decoded, err := hex.DecodeString(id)
		if err != nil || len(decoded) != 16 {
			t.Fatalf("request ID = %q, want 16 bytes encoded as hex", id)
		}
	}
}

func TestLogger(t *testing.T) {
	var output bytes.Buffer
	log := logger.New(config.LogConfig{Level: "info", Format: "json"}, &output)
	log.Debug("filtered")
	engine := gin.New()
	engine.Use(RequestID(), Logger(log))
	engine.GET("/logged", func(c *gin.Context) {
		_ = c.Error(errors.New("missing data"))
		c.Status(http.StatusBadRequest)
	})

	req := httptest.NewRequest(http.MethodGet, "/logged?ignore=query", nil)
	req.Header.Set(RequestIDHeader, "logged-id")
	req.Header.Set("User-Agent", "tool\n"+strings.Repeat("x", 300))
	req.Header.Set("Authorization", "Bearer log-secret")
	req.Header.Set("Cookie", "session=cookie-secret")
	req.RemoteAddr = "192.0.2.10:1234"
	engine.ServeHTTP(httptest.NewRecorder(), req)

	for _, secret := range []string{"ignore=query", "log-secret", "cookie-secret", "missing data"} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("log contains %q", secret)
		}
	}
	record := decodeJSON(t, &output)
	want := map[string]any{
		"level": "WARN", "request_id": "logged-id", "method": "GET", "path": "/logged",
		"status": float64(http.StatusBadRequest), "client_ip": "192.0.2.10", "route": "/logged", "auth": "disabled",
		"bytes_in": float64(0), "bytes_out": float64(0), "user_agent": "tool " + strings.Repeat("x", 251),
	}
	for name, value := range want {
		if !reflect.DeepEqual(record[name], value) {
			t.Errorf("%s = %#v, want %#v", name, record[name], value)
		}
	}
	if ms, ok := record["latency_ms"].(float64); !ok || ms < 0 {
		t.Errorf("latency_ms = %#v, want non-negative number", record["latency_ms"])
	}
}

func TestRecovery(t *testing.T) {
	var output bytes.Buffer
	log := logger.New(config.LogConfig{Level: "info", Format: "json"}, &output)
	engine := gin.New()
	engine.Use(RequestID(), Logger(log), Recovery(log))
	engine.GET("/panic", func(*gin.Context) { panic("test panic") })
	engine.GET("/ok", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	req.Header.Set(RequestIDHeader, "panic-id")
	resp := httptest.NewRecorder()
	engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusInternalServerError || !strings.HasPrefix(resp.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("status = %d, headers = %v, body = %s", resp.Code, resp.Header(), resp.Body)
	}
	want := map[string]any{"error": "internal server error", "request_id": "panic-id", "code": "internal_error"}
	if got := decodeJSON(t, resp.Body); !reflect.DeepEqual(got, want) {
		t.Fatalf("response = %v, want %v", got, want)
	}
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("got %d log lines, want panic and access log", len(lines))
	}
	record := decodeJSON(t, bytes.NewBuffer(lines[0]))
	stack, _ := record["stack"].(string)
	if strings.Contains(output.String(), "test panic") || record["request_id"] != "panic-id" || !strings.Contains(stack, "debug.Stack") {
		t.Fatalf("panic log = %v", record)
	}
	if record := decodeJSON(t, bytes.NewBuffer(lines[1])); record["status"] != float64(http.StatusInternalServerError) || record["level"] != "ERROR" {
		t.Fatalf("access log = %v, want status 500", record)
	}

	resp = httptest.NewRecorder()
	engine.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/ok", nil))
	if resp.Code != http.StatusNoContent {
		t.Fatalf("status after panic = %d, want 204", resp.Code)
	}
}

func TestCORS(t *testing.T) {
	for _, tc := range []struct {
		name, method, origin, allowedOrigin string
		origins                             []string
		status                              int
	}{
		{"allowed", "GET", "https://app.example", "https://app.example", []string{"https://app.example"}, 200},
		{"denied", "GET", "https://other.example", "", []string{"https://app.example"}, 200},
		{"preflight", "OPTIONS", "https://app.example", "https://app.example", []string{"https://app.example"}, 204},
		{"denied preflight", "OPTIONS", "https://other.example", "", []string{"https://app.example"}, 204},
		{"wildcard", "GET", "https://other.example", "*", []string{"*"}, 200},
		{"wildcard without origin", "GET", "", "*", []string{"*"}, 200},
		{"no origin", "GET", "", "", []string{"https://app.example"}, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := gin.New()
			engine.Use(CORS(config.CORSConfig{
				AllowedOrigins: tc.origins,
				AllowedMethods: []string{"GET", "OPTIONS"},
				AllowedHeaders: []string{"Content-Type", "X-Request-ID"},
			}))
			engine.GET("/", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
			engine.OPTIONS("/", func(*gin.Context) { t.Error("preflight reached handler") })
			req := httptest.NewRequest(tc.method, "/", nil)
			req.Header.Set("Origin", tc.origin)
			resp := httptest.NewRecorder()
			engine.ServeHTTP(resp, req)

			if resp.Code != tc.status || resp.Header().Get("Access-Control-Allow-Origin") != tc.allowedOrigin {
				t.Fatalf("status = %d, headers = %v", resp.Code, resp.Header())
			}
			if tc.allowedOrigin != "" &&
				(resp.Header().Get("Access-Control-Allow-Methods") != "GET, OPTIONS" ||
					resp.Header().Get("Access-Control-Allow-Headers") != "Content-Type, X-Request-ID") {
				t.Fatalf("missing CORS headers: %v", resp.Header())
			}
			if tc.origins[0] != "*" && resp.Header().Get("Vary") != "Origin" {
				t.Fatalf("Vary = %q, want Origin", resp.Header().Get("Vary"))
			}
			if tc.status == http.StatusNoContent && resp.Body.Len() != 0 {
				t.Fatalf("preflight body = %q, want empty", resp.Body)
			}
		})
	}
}

func decodeJSON(t *testing.T, body *bytes.Buffer) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(body.Bytes(), &result); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return result
}
