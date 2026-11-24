package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"projectlens/api"
	"projectlens/internal/config"
	"projectlens/internal/handler"
)

func TestOpenAPIRouteParity(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{
		App:  config.AppConfig{Env: "test"},
		Auth: config.AuthConfig{Enabled: true, BearerTokens: []string{"test-token"}},
	}
	engine := New(cfg, log, Dependencies{
		Projects: handler.NewProjectHandler(nil, log), Repositories: handler.NewRepositoryHandler(nil, log),
		Activity: &handler.ActivityHandler{Log: log}, History: &handler.HistoryHandler{Log: log},
		Snapshots: handler.NewSnapshotHandler(nil, log), Documents: handler.NewDocumentHandler(nil, log, 0),
		Jobs: handler.NewJobHandler(nil, log), Search: handler.NewSearchHandler(nil, log),
	})
	var spec map[string]any
	if err := yaml.Unmarshal(api.OpenAPIYAML(), &spec); err != nil {
		t.Fatal(err)
	}
	info := object(t, spec["info"])
	components := object(t, spec["components"])
	bearer := object(t, object(t, components["securitySchemes"])["BearerAuth"])
	if spec["openapi"] != "3.0.3" || info["title"] == "" || info["version"] == "" ||
		bearer["type"] != "http" || bearer["scheme"] != "bearer" {
		t.Fatal("invalid OpenAPI metadata or bearer scheme")
	}
	checkReferences(t, spec, spec)

	routeParam := regexp.MustCompile(`:([^/]+)`)
	specParam := regexp.MustCompile(`\{([^}]+)\}`)
	want := map[string]bool{}
	for _, route := range engine.Routes() {
		want[route.Method+" "+routeParam.ReplaceAllString(route.Path, `{${1}}`)] = true
	}
	operations := map[string]bool{}
	for path, raw := range object(t, spec["paths"]) {
		item := object(t, raw)
		for method, raw := range item {
			if method == "parameters" {
				continue
			}
			operation := object(t, raw)
			route := strings.ToUpper(method) + " " + path
			if !want[route] {
				t.Errorf("documented route %s is unregistered", route)
			}
			delete(want, route)
			opID, _ := operation["operationId"].(string)
			if opID == "" || operations[opID] {
				t.Errorf("%s lacks a unique operationId", route)
			}
			operations[opID] = true
			params := map[string]bool{}
			for _, owner := range []map[string]any{item, operation} {
				entries, _ := owner["parameters"].([]any)
				for _, entry := range entries {
					parameter := resolved(t, spec, object(t, entry))
					if parameter["in"] == "path" && parameter["required"] == true {
						params[fmt.Sprint(parameter["name"])] = true
					}
				}
			}
			for _, match := range specParam.FindAllStringSubmatch(path, -1) {
				if !params[match[1]] {
					t.Errorf("%s missing required parameter %s", route, match[1])
				}
			}
			security, _ := operation["security"].([]any)
			if strings.HasPrefix(path, "/api/v1/") {
				if len(security) != 1 || !reflect.DeepEqual(object(t, security[0])["BearerAuth"], []any{}) {
					t.Errorf("%s missing bearer security", route)
				}
			} else if len(security) != 0 {
				t.Errorf("%s should be public", route)
			}
			responses := object(t, operation["responses"])
			if len(responses) == 0 {
				t.Errorf("%s missing responses", route)
			}
			for code, raw := range responses {
				response := resolved(t, spec, object(t, raw))
				if desc, ok := response["description"].(string); !ok || desc == "" {
					t.Errorf("%s response %s missing description", route, code)
				}
			}
		}
	}
	for route := range want {
		t.Errorf("registered route %s missing from OpenAPI", route)
	}
	for _, endpoint := range []struct{ path, contentType string }{
		{"/openapi.yaml", "application/yaml; charset=utf-8"},
		{"/openapi.json", "application/json; charset=utf-8"},
	} {
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, endpoint.path, nil))
		if response.Code != 200 || response.Header().Get("Content-Type") != endpoint.contentType {
			t.Fatalf("%s returned %d %v", endpoint.path, response.Code, response.Header())
		}
		if endpoint.path == "/openapi.yaml" {
			if !bytes.Equal(api.OpenAPIYAML(), response.Body.Bytes()) {
				t.Fatal("YAML endpoint does not match the embedded specification")
			}
		} else {
			// Marshal the YAML parse as JSON so numeric values share decoder semantics.
			wantJSON, err := json.Marshal(spec)
			if err != nil || !json.Valid(response.Body.Bytes()) || !bytes.Equal(wantJSON, response.Body.Bytes()) {
				t.Fatal("JSON endpoint changed the embedded specification")
			}
		}
	}
}

func object(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected an OpenAPI object, got %T", value)
	}
	return result
}

func resolved(t *testing.T, spec, value map[string]any) map[string]any {
	t.Helper()
	pointer, ok := value["$ref"].(string)
	if !ok {
		return value
	}
	if !strings.HasPrefix(pointer, "#/") {
		t.Fatalf("unsupported reference %q", pointer)
	}
	current := spec
	for _, key := range strings.Split(strings.TrimPrefix(pointer, "#/"), "/") {
		if _, ok := current[key]; !ok {
			t.Fatalf("unresolved reference %q", pointer)
		}
		current = object(t, current[key])
	}
	return current
}

func checkReferences(t *testing.T, spec map[string]any, value any) {
	t.Helper()
	switch value := value.(type) {
	case map[string]any:
		resolved(t, spec, value)
		for _, entry := range value {
			checkReferences(t, spec, entry)
		}
	case []any:
		for _, entry := range value {
			checkReferences(t, spec, entry)
		}
	}
}
