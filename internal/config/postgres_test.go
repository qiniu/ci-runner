package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"gopkg.in/yaml.v3"
)

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
