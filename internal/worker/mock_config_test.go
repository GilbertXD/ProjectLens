package worker

import (
	"testing"

	"projectlens/internal/config"
)

func TestConfiguredMockBoundary(t *testing.T) {
	for _, env := range []string{"development", "test", "production", "staging", ""} {
		for _, enabled := range []bool{false, true} {
			reg := &Registry{}
			cfg := config.Config{App: config.AppConfig{Env: env}, Worker: config.WorkerConfig{EnableMockJobs: enabled}}
			if err := RegisterConfiguredMocks(reg, cfg); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"mock.echo", "mock.sleep", "mock.fail", "mock.flaky"} {
				if reg.Has(name) != (enabled && (env == "development" || env == "test")) {
					t.Fatal(env, enabled, name)
				}
			}
		}
	}
}
