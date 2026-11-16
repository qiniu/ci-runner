package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/state"
)

func TestRunnerRegistrationLookupReadOnly(t *testing.T) {
	for _, scenario := range []string{"found", "absent", "organization", "pagination", "forbidden", "not found API", "rate limited", "malformed", "null row", "missing name", "invalid ID", "anonymous", "user", "missing request", "missing target"} {
		t.Run(scenario, func(t *testing.T) {
			store := state.New(t.TempDir())
			_, st, err := store.CreateRequest(state.RunnerRequest{ID: "123", RunnerName: "e2b-123", RepositoryFullName: "o/r"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			st.Status = state.StatusStopping
			st.ProvisionAttemptID = "exact-attempt"
			st.WatchToken = "cleanup-owner"
			st.LastErrorCode = "sandbox_create_outcome_unknown"
			if scenario == "organization" {
				st.RunnerGroup = "build"
			}
			if scenario == "missing target" {
				st.RunnerName = ""
			}
			if err := store.WriteState(st); err != nil {
				t.Fatal(err)
			}
			before, _ := store.ReadState(st.ID)
			var calls atomic.Int32
			gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				path := "/repos/o/r/actions/runners"
				if scenario == "organization" {
					path = "/orgs/o/actions/runners"
				}
				if r.Method != "GET" || r.URL.Path != path || r.URL.Query().Get("name") != "e2b-123" {
					t.Errorf("unexpected GitHub operation: %s %s", r.Method, r.URL)
					http.Error(w, "unexpected operation", http.StatusInternalServerError)
					return
				}
				switch scenario {
				case "forbidden":
					http.Error(w, "secret-canary", http.StatusForbidden)
				case "not found API":
					http.Error(w, "secret-canary", http.StatusNotFound)
				case "rate limited":
					http.Error(w, "secret-canary", http.StatusTooManyRequests)
				case "malformed":
					fmt.Fprint(w, `{}`)
				case "null row":
					fmt.Fprint(w, `{"runners":[null]}`)
				case "missing name":
					fmt.Fprint(w, `{"runners":[{"id":42}]}`)
				case "invalid ID":
					fmt.Fprint(w, `{"runners":[{"id":0,"name":"e2b-123"}]}`)
				case "absent":
					fmt.Fprint(w, `{"runners":[{"id":12,"name":"e2b-123-other"}]}`)
				default:
					if scenario == "pagination" && r.URL.Query().Get("page") == "" {
						w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?name=e2b-123&page=2>; rel="next"`, r.Host, path))
						fmt.Fprint(w, `{"runners":[]}`)
						return
					}
					fmt.Fprint(w, `{"runners":[{"id":42,"name":"e2b-123","status":"offline","busy":false,"labels":[{"name":"secret-canary"}]}]}`)
				}
			}))
			defer gh.Close()
			fake := &fakeSandbox{}
			srv := newTestServerWithoutLoops(t, store, gh.URL, fake, 1)
			// All store operations other than these two reads panic. This catches
			// lease/checkpoint writes even when they do not change the business version.
			srv.store = &registrationReadOnlyStore{reader: store}
			url := "/runner_requests/123/github-runner?repository_full_name=attacker/repo&runner_name=other&runner_group=other"
			if scenario == "missing request" {
				url = "/runner_requests/missing/github-runner"
			}
			req := adminRequest("GET", url, nil)
			if scenario == "anonymous" {
				req.Header.Del("Cookie")
			}
			if scenario == "user" {
				req.Header.Del("Cookie")
				req.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
			}
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			wantCode := 200
			switch scenario {
			case "forbidden", "not found API", "rate limited", "malformed", "null row", "missing name", "invalid ID":
				wantCode = 502
			case "anonymous", "user":
				wantCode = 401
			case "missing request":
				wantCode = 404
			case "missing target":
				wantCode = 409
			}
			if rec.Code != wantCode {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, wantCode, rec.Body.String())
			}
			if rec.Header().Get("Cache-Control") != "no-store" || strings.Contains(rec.Body.String(), "secret-canary") {
				t.Fatal("unsafe response", rec.Body.String())
			}
			if wantCode == 200 {
				var out runnerRegistrationLookup
				if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				if out.ObservedAt.IsZero() || out.RunnerName != "e2b-123" || out.RepositoryFullName != "o/r" {
					t.Fatalf("bad identity: %+v", out)
				}
				if scenario == "absent" {
					if out.LookupStatus != "not_found" || out.Runner != nil {
						t.Fatalf("bad absence: %+v", out)
					}
				} else if out.LookupStatus != "found" || out.Runner == nil || out.Runner.ID != 42 || out.Runner.Status != "offline" || out.Runner.Busy {
					t.Fatalf("bad registration: %+v", out)
				}
				if scenario == "organization" {
					if out.Scope != "organization" || out.Target != "o" || out.RunnerGroup != "build" {
						t.Fatalf("bad org scope: %+v", out)
					}
				} else if out.Scope != "repository" || out.Target != "o/r" {
					t.Fatalf("bad repo scope: %+v", out)
				}
			}
			wantCalls := int32(1)
			if wantCode == 401 || wantCode == 404 || wantCode == 409 {
				wantCalls = 0
			}
			if scenario == "pagination" {
				wantCalls = 2
			}
			if calls.Load() != wantCalls {
				t.Fatalf("remote calls=%d want=%d", calls.Load(), wantCalls)
			}
			after, _ := store.ReadState(st.ID)
			audits, _ := store.ListAuditEvents(10)
			events, _, _ := store.ListRunnerEvents(st.ID, 0, 100, "")
			if !reflect.DeepEqual(before, after) || len(audits) != 0 || len(events) != 0 || fake.stoppedCount() != 0 {
				t.Fatalf("lookup mutated state, events, audit or Sandbox: before=%+v after=%+v audits=%d events=%d", before, after, len(audits), len(events))
			}
		})
	}
}

type registrationReadOnlyStore struct {
	state.Store
	reader state.Store
}

func (s *registrationReadOnlyStore) ReadState(id string) (state.RunnerState, error) {
	return s.reader.ReadState(id)
}

func (s *registrationReadOnlyStore) GetAccountByOAuthIdentity(provider, subject string) (state.Account, state.OAuthIdentity, error) {
	return s.reader.GetAccountByOAuthIdentity(provider, subject)
}

func TestRunnerRegistrationLookupDeadline(t *testing.T) {
	store := state.New(t.TempDir())
	if _, _, err := store.CreateRequest(state.RunnerRequest{ID: "123", RunnerName: "e2b-123", RepositoryFullName: "o/r"}, nil); err != nil {
		t.Fatal(err)
	}
	srv := newTestServerWithoutLoops(t, store, "https://api.github.com", &fakeSandbox{}, 1)
	srv.store = &registrationReadOnlyStore{reader: store}
	called := false
	srv.gh = github.NewClient("https://api.github.com", &http.Client{Transport: profileValidationTransportFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 10*time.Second {
			t.Error("lookup must have a deadline no later than ten seconds")
		}
		return nil, context.DeadlineExceeded
	})})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, adminRequest("GET", "/runner_requests/123/github-runner", nil))
	if !called || rec.Code != 502 || strings.Contains(rec.Body.String(), "not_found") {
		t.Fatalf("deadline must fail closed: status=%d body=%s", rec.Code, rec.Body.String())
	}
}
