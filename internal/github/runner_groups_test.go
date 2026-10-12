package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestListOrganizationRunnerGroupsUsesExactInstallation(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/app/installations/456/access_tokens":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				t.Error("missing app authorization")
			}
			fmt.Fprintf(w, `{"token":"installation-456","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
		case "/orgs/octo-org/actions/runner-groups":
			if r.Header.Get("Authorization") != "token installation-456" {
				t.Errorf("unexpected authorization: %q", r.Header.Get("Authorization"))
			}
			if r.URL.Query().Get("visible_to_repository") != "repo" {
				t.Error("missing exact repository filter")
			}
			fmt.Fprint(w, `{"total_count":1,"runner_groups":[{"id":1,"name":"Default"}]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	client, err := NewAppClient(ts.URL, AppAuth{AppID: 123, PrivateKeyFile: testPrivateKeyFile(t)}, ts.Client())
	if err != nil {
		t.Fatal(err)
	}
	groups, err := client.ListOrganizationRunnerGroups(t.Context(), 456, "octo-org", "repo")
	if err != nil || len(groups) != 1 || calls.Load() != 2 {
		t.Fatalf("groups=%v err=%v calls=%d", groups, err, calls.Load())
	}
}

func TestRunnerGroupValidation(t *testing.T) {
	t.Run("empty group keeps repository registration without lookup", func(t *testing.T) {
		if err := NewClient("http://unused.invalid", http.DefaultClient).ValidateRunnerGroup(t.Context(), 0, "org/repo", " "); err != nil {
			t.Fatal(err)
		}
	})
	for _, tc := range []struct {
		name, group, response, visible string
		want                           error
	}{
		{"allowed", "build", `{"id":4,"name":"Build"}`, `{"id":4,"name":"Build"}`, nil},
		{"missing", "missing", `{"id":4,"name":"Build"}`, `{"id":4,"name":"Build"}`, ErrRunnerGroupNotFound},
		{"repository denied", "Build", `{"id":4,"name":"Build"}`, "", ErrRunnerGroupRepositoryDenied},
		{"inherited", "Build", `{"id":4,"name":"Build","inherited":true}`, "", ErrRunnerGroupInherited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				value := tc.response
				if r.URL.Query().Get("visible_to_repository") != "" {
					value = tc.visible
				}
				count := 1
				if value == "" {
					count = 0
				}
				fmt.Fprintf(w, `{"total_count":%d,"runner_groups":[%s]}`, count, value)
			}))
			defer ts.Close()
			err := NewClient(ts.URL, ts.Client()).ValidateRunnerGroup(t.Context(), 1, "org/repo", tc.group)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want %v", err, tc.want)
			}
		})
	}
}

func TestRunnerGroupValidationResolvesManualAppInstallation(t *testing.T) {
	for _, staticID := range []int64{0, 456} {
		t.Run(fmt.Sprint(staticID), func(t *testing.T) {
			var lookups atomic.Int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/octo-org/repo/installation":
					lookups.Add(1)
					fmt.Fprint(w, `{"id":456}`)
				case "/app/installations/456/access_tokens":
					fmt.Fprintf(w, `{"token":"manual-installation","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
				case "/orgs/octo-org/actions/runner-groups":
					if r.Header.Get("Authorization") != "token manual-installation" {
						t.Error("wrong installation credentials")
					}
					fmt.Fprint(w, `{"total_count":1,"runner_groups":[{"id":1,"name":"Build"}]}`)
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer ts.Close()
			client, err := NewAppClient(ts.URL, AppAuth{AppID: 123, InstallationID: staticID, PrivateKeyFile: testPrivateKeyFile(t)}, ts.Client())
			if err != nil {
				t.Fatal(err)
			}
			if err := client.ValidateRunnerGroup(t.Context(), 0, "octo-org/repo", "Build"); err != nil {
				t.Fatal(err)
			}
			if (staticID == 0 && lookups.Load() != 1) || (staticID != 0 && lookups.Load() != 0) {
				t.Fatalf("unexpected dynamic lookups=%d", lookups.Load())
			}
		})
	}
}

func TestRunnerGroupsPaginationAndBounds(t *testing.T) {
	t.Run("local pagination ignores foreign Link", func(t *testing.T) {
		var calls int
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if calls == 1 {
				w.Header().Set("Link", `<https://untrusted.invalid/steal>; rel="next"`)
				fmt.Fprint(w, `{"total_count":2,"runner_groups":[{"id":1,"name":"one"}]}`)
			} else {
				if r.URL.Query().Get("page") != "2" {
					t.Error("wrong page")
				}
				fmt.Fprint(w, `{"total_count":2,"runner_groups":[{"id":2,"name":"two"}]}`)
			}
		}))
		defer ts.Close()
		groups, err := NewClient(ts.URL, ts.Client()).ListOrganizationRunnerGroups(t.Context(), 1, "org", "")
		if err != nil || len(groups) != 2 || calls != 2 {
			t.Fatalf("groups=%v err=%v calls=%d", groups, err, calls)
		}
	})
	for _, value := range []string{`{}`, `{"total_count":1,"runner_groups":[]}`, `{"total_count":1,"runner_groups":[{"id":0,"name":"x"}]}`, strings.Repeat("x", (1<<20)+1)} {
		t.Run(fmt.Sprint(len(value)), func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, value) }))
			defer ts.Close()
			if _, err := NewClient(ts.URL, ts.Client()).ListOrganizationRunnerGroups(t.Context(), 1, "org", ""); err == nil {
				t.Fatal("expected bounded invalid response rejection")
			}
		})
	}
	t.Run("page ceiling", func(t *testing.T) {
		var calls int
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			fmt.Fprint(w, `{"total_count":1000,"runner_groups":[{"id":1,"name":"x"}]}`)
		}))
		defer ts.Close()
		if _, err := NewClient(ts.URL, ts.Client()).ListOrganizationRunnerGroups(t.Context(), 1, "org", ""); err == nil || calls != 10 {
			t.Fatalf("err=%v calls=%d", err, calls)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := NewClient("http://unused.invalid", http.DefaultClient).ListOrganizationRunnerGroups(ctx, 1, "org", "")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("caller deadline bounds pending response", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
		defer ts.Close()
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
		defer cancel()
		_, err := NewClient(ts.URL, ts.Client()).ListOrganizationRunnerGroups(ctx, 1, "org", "")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err=%v", err)
		}
	})
}
