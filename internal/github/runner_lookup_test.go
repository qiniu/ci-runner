package github

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFindRunnerByNameUsesAppInstallation(t *testing.T) {
	for _, group := range []string{"", "build"} {
		t.Run("group="+group, func(t *testing.T) {
			path := "/repos/o/r/actions/runners"
			if group != "" {
				path = "/orgs/o/actions/runners"
			}
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "GET" && r.URL.Path == "/repos/o/r/installation":
					fmt.Fprint(w, `{"id":456}`)
				case r.Method == "POST" && r.URL.Path == "/app/installations/456/access_tokens":
					fmt.Fprintf(w, `{"token":"installation-token","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
				case r.Method == "GET" && r.URL.Path == path:
					if r.Header.Get("Authorization") != "token installation-token" || r.URL.Query().Get("name") != "e2b-123" {
						t.Error("wrong auth or name filter")
					}
					fmt.Fprint(w, `{"runners":[{"id":12,"name":"e2b-123","status":"online","busy":true}]}`)
				default:
					t.Errorf("unexpected operation %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer ts.Close()
			client, err := NewAppClient(ts.URL, AppAuth{AppID: 123, PrivateKeyFile: testPrivateKeyFile(t)}, ts.Client())
			if err != nil {
				t.Fatal(err)
			}
			runner, err := client.FindRunnerByName(t.Context(), "o/r", group, "e2b-123")
			if err != nil || runner == nil || runner.ID != 12 || runner.Status != "online" || !runner.Busy {
				t.Fatalf("runner=%+v err=%v", runner, err)
			}
		})
	}
}
