package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunnerConcurrencyConfig(t *testing.T) {
	base, err := os.ReadFile("../../runnerd.yaml.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, accounts, repositories string
		invalid                      bool
	}{
		{"empty", "{}", "{}", false},
		{"normalized", "{Acme: 3, other: 0}", "{Acme/Build.Tools: 2}", false},
		{"negative_account", "{acme: -1}", "{}", true},
		{"negative_repository", "{}", "{acme/repo: -1}", true},
		{"invalid_owner", "{'acme/*': 2}", "{}", true},
		{"invalid_repository", "{}", "{acme: 2}", true},
		{"wildcard_repository", "{}", "{'acme/*': 2}", true},
		{"duplicate_owner", "{Acme: 2, acme: 3}", "{}", true},
		{"duplicate_repository", "{}", "{Acme/Repo: 2, acme/repo: 3}", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := strings.Replace(string(base), "account_concurrency_limits: {}", "account_concurrency_limits: "+tc.accounts, 1)
			data = strings.Replace(data, "repository_concurrency_limits: {}", "repository_concurrency_limits: "+tc.repositories, 1)
			path := filepath.Join(t.TempDir(), "runnerd.yaml")
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadFile(path)
			if tc.invalid {
				if err == nil || !strings.Contains(err.Error(), "concurrency_limits") {
					t.Fatalf("expected invalid limit error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "empty" && (len(cfg.RunnerAccountConcurrency) != 0 || len(cfg.RunnerRepositoryConcurrency) != 0) {
				t.Fatal("missing limits must preserve unlimited scope behavior")
			}
			if tc.name == "normalized" && (cfg.RunnerAccountConcurrency["acme"] != 3 || cfg.RunnerRepositoryConcurrency["acme/build.tools"] != 2) {
				t.Fatal("limits were not normalized")
			}
		})
	}
}
