package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"gopkg.in/yaml.v3"
)

func TestLoadFilePostgresPoolBudget(t *testing.T) {
	for _, tc := range []struct {
		backend string
		limit   int
		invalid bool
	}{{"postgres", 0, false}, {"postgres", 4, false}, {"postgres", -1, true}, {"sqlite", 4, true}, {"mysql", 4, true}} {
		t.Run(fmt.Sprintf("%s/%d", tc.backend, tc.limit), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runnerd.yaml")
			data := fmt.Sprintf(`database: {backend: %s, dsn: fixture, postgres_max_open_connections: %d}
auth: {session_secret: fixture, encryption_key: fixture}
github: {token: fixture, webhook_secret: fixture, oauth: {client_id: fixture, client_secret: fixture}}
sandbox: {regions: [{id: local, label: Local, sandbox_api_url: "https://sandbox.example.test"}]}
`, tc.backend, tc.limit)
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadFile(path)
			if tc.invalid {
				if err == nil || !strings.Contains(err.Error(), "database.postgres_max_open_connections") {
					t.Fatalf("expected pool limit validation, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.PostgresMaxOpenConns != tc.limit {
				t.Fatalf("got %d, want %d", cfg.PostgresMaxOpenConns, tc.limit)
			}
		})
	}
}

func TestParsePostgresDSNPasswordFormats(t *testing.T) {
	password := "spaces 'quotes' \\ :/@?&= 中文"
	encoded, err := ObfuscateSecret([]byte(password))
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string {
		return "'" + strings.NewReplacer("\\", "\\\\", "'", "\\'").Replace(s) + "'"
	}
	keyword := func(p string) string {
		return "host=127.0.0.1 port=5433 user=runner dbname=runner_test sslmode=disable search_path=private_schema connect_timeout=4 password=" + quote(p)
	}
	uri := func(p string) string {
		u := url.URL{Scheme: "postgres", Host: "127.0.0.1:5433", Path: "/runner_test", User: url.UserPassword("runner", p)}
		u.RawQuery = url.Values{"sslmode": {"disable"}, "search_path": {"private_schema"}, "connect_timeout": {"4"}}.Encode()
		return u.String()
	}
	for _, format := range []struct {
		name string
		make func(string) string
	}{{"keyword", keyword}, {"URI", uri}} {
		for _, mode := range []string{"plaintext", "password", "whole DSN"} {
			t.Run(format.name+"/"+mode, func(t *testing.T) {
				dsn := format.make(password)
				if mode == "password" {
					dsn = format.make(encoded)
				}
				if mode == "whole DSN" {
					dsn, err = ObfuscateSecret([]byte(dsn))
					if err != nil {
						t.Fatal(err)
					}
				}
				data, err := yaml.Marshal(map[string]string{"dsn": dsn})
				if err != nil {
					t.Fatal(err)
				}
				var raw struct {
					DSN Secret `yaml:"dsn"`
				}
				if err := yaml.Unmarshal(data, &raw); err != nil {
					t.Fatal(err)
				}
				parsed, err := ParsePostgresDSN(raw.DSN)
				if err != nil {
					t.Fatal(err)
				}
				if parsed.Password != password || parsed.Host != "127.0.0.1" || parsed.Port != 5433 || parsed.User != "runner" || parsed.Database != "runner_test" {
					t.Fatal("connection fields changed")
				}
				if parsed.RuntimeParams["search_path"] != "private_schema" || parsed.ConnectTimeout.Seconds() != 4 {
					t.Fatal("connection options lost")
				}
				if fmt.Sprint(raw.DSN) != maskedSecret {
					t.Fatal("DSN no longer masked")
				}
			})
		}
	}
}

func TestParsePostgresDSNRedactsErrors(t *testing.T) {
	for _, dsn := range []string{
		"host=private-host password=private-password invalid",
		"host=private-host password=RUNNERD_ENC(v1:private-ciphertext)",
		"RUNNERD_ENC(v1:private-ciphertext)",
	} {
		_, err := ParsePostgresDSN(Secret(dsn))
		var parseErr *pgconn.ParseConfigError
		if err == nil || !errors.As(err, &parseErr) {
			t.Fatal("expected classified configuration failure")
		}
		if strings.Contains(fmt.Sprintf("%+v", err), "private-") || parseErr.ConnString != "[redacted]" || errors.Unwrap(err) != nil {
			t.Fatal("configuration error leaked input")
		}
	}
}
