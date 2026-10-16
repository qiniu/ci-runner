package state

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDeploymentEventAfterCursorPreservesEveryPage(t *testing.T) {
	store := New(t.TempDir())
	account, _, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "event-user", OAuthLogin: "event-user"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	d, err := store.CreateDeployment(deploymentFixture("cursor-deployment", account.ID))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 7 {
		if err := store.AppendDeploymentEvent(DeploymentEvent{DeploymentID: d.ID, EventType: "output", Message: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	latest, more, err := store.ListDeploymentEvents(d.ID, 0, 2)
	if err != nil || !more || len(latest) != 2 || latest[0].Message != "5" || latest[1].Message != "6" {
		t.Fatalf("latest=%#v more=%v err=%v", latest, more, err)
	}
	older, more, err := store.ListDeploymentEvents(d.ID, latest[0].ID, 2)
	if err != nil || !more || len(older) != 2 || older[0].Message != "3" || older[1].Message != "4" {
		t.Fatalf("older=%#v more=%v err=%v", older, more, err)
	}
	after, more, err := store.ListDeploymentEventsAfter(d.ID, older[0].ID, 2)
	if err != nil || !more || len(after) != 2 || after[0].Message != "4" || after[1].Message != "5" {
		t.Fatalf("after=%#v more=%v err=%v", after, more, err)
	}
	last, more, err := store.ListDeploymentEventsAfter(d.ID, after[1].ID, 2)
	if err != nil || more || len(last) != 1 || last[0].Message != "6" {
		t.Fatalf("last=%#v more=%v err=%v", last, more, err)
	}
}

func deploymentFixture(id string, accountID int64) Deployment {
	return Deployment{
		ID:                     id,
		CreateRequestHash:      strings.Repeat("c", 64),
		AccountID:              accountID,
		ScopeType:              AccountScopeTypeAccount,
		ScopeID:                accountID,
		GitHubInstallationID:   100 + accountID,
		RepositoryID:           200 + accountID,
		RepositoryFullName:     "owner/project",
		Ref:                    "main",
		CommitSHA:              strings.Repeat("a", 40),
		ConfigPath:             ".qiniu/deploy.yaml",
		SnapshotVersion:        1,
		SpecHash:               "sha256:" + strings.Repeat("b", 64),
		SpecJSONEncrypted:      "encrypted-spec",
		DeploymentTemplateName: "base",
		ExpiresAt:              time.Now().UTC().Add(time.Hour),
	}
}

func TestDeploymentStorePersistsLifecycleAndRejectsStaleUpdates(t *testing.T) {
	store := New(t.TempDir())
	created, err := store.CreateDeployment(deploymentFixture("deployment-1", 1))
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != DeploymentStatusQueued || created.Version != 0 || !created.PlanningAt.IsZero() {
		t.Fatalf("created deployment = %#v", created)
	}

	created.Status = DeploymentStatusPlanning
	if err := store.UpdateDeployment(created); err != nil {
		t.Fatalf("UpdateDeployment(planning): %v", err)
	}
	planning, err := store.GetDeployment(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if planning.Status != DeploymentStatusPlanning || planning.Version != 1 || planning.PlanningAt.IsZero() {
		t.Fatalf("planning deployment = %#v", planning)
	}

	stale := planning
	stale.Status = DeploymentStatusCreating
	if err := store.UpdateDeployment(stale); err != nil {
		t.Fatalf("UpdateDeployment(stale): %v", err)
	}
	if err := store.UpdateDeployment(stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("second stale UpdateDeployment() error = %v, want ErrConflict", err)
	}

	current, err := store.GetDeployment(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.Status = DeploymentStatusRunning
	current.SandboxID = "sandbox-1"
	current.Endpoint = "https://sandbox.example.test"
	if err := store.UpdateDeployment(current); err != nil {
		t.Fatalf("UpdateDeployment(running): %v", err)
	}
	running, err := store.GetDeployment(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if running.Status != DeploymentStatusRunning || running.RunningAt.IsZero() || running.SandboxID != "sandbox-1" {
		t.Fatalf("running deployment = %#v", running)
	}

	running.Status = DeploymentStatusCompleted
	if err := store.UpdateDeployment(running); err != nil {
		t.Fatalf("UpdateDeployment(completed): %v", err)
	}
	completed, err := store.GetDeployment(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.CompletedAt.IsZero() || completed.Status != DeploymentStatusCompleted {
		t.Fatalf("completed deployment = %#v", completed)
	}
}

func TestDeploymentStoreListsOnlyTheRequestedAccount(t *testing.T) {
	store := New(t.TempDir())
	for _, fixture := range []Deployment{
		deploymentFixture("account-1-a", 1),
		deploymentFixture("account-1-b", 1),
		deploymentFixture("account-2-a", 2),
	} {
		if _, err := store.CreateDeployment(fixture); err != nil {
			t.Fatal(err)
		}
	}
	items, total, err := store.ListDeploymentsForAccount(1, DeploymentListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(items) != 1 || items[0].AccountID != 1 {
		t.Fatalf("account deployment page = %#v total=%d", items, total)
	}
	active, err := store.ListActiveDeployments()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 3 {
		t.Fatalf("active deployments = %#v, want 3", active)
	}
}

func TestDeploymentStoreRejectsInvalidImmutableEvidence(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Deployment)
	}{
		{name: "missing commit", edit: func(d *Deployment) { d.CommitSHA = "" }},
		{name: "unsafe config path", edit: func(d *Deployment) { d.ConfigPath = "../deploy.yaml" }},
		{name: "invalid scope", edit: func(d *Deployment) { d.ScopeType = "organization" }},
		{name: "missing plan", edit: func(d *Deployment) { d.SpecJSONEncrypted = "" }},
		{name: "invalid repository", edit: func(d *Deployment) { d.RepositoryFullName = "owner" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := deploymentFixture(tt.name, 1)
			tt.edit(&fixture)
			if _, err := New(t.TempDir()).CreateDeployment(fixture); err == nil {
				t.Fatal("CreateDeployment() succeeded for invalid evidence")
			}
		})
	}
}

func TestFreshSchemaIncludesDeployments(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasTable(&deploymentRecord{}) {
		t.Fatal("fresh schema missing deployments table")
	}
	for _, index := range []string{
		"idx_deployments_account_created",
		"idx_deployments_scope",
		"idx_deployments_status_expires",
		"idx_deployments_sandbox",
	} {
		if !db.Migrator().HasIndex(&deploymentRecord{}, index) {
			t.Fatalf("fresh schema missing deployment index %s", index)
		}
	}
	if !db.Migrator().HasTable(&deploymentEventRecord{}) {
		t.Fatal("fresh schema missing deployment_events table")
	}
}

func TestDeploymentRetryResetsAttemptEvidenceAndEventsPage(t *testing.T) {
	store := New(t.TempDir())
	fixture := deploymentFixture("retry-deployment", 1)
	fixture.Status = DeploymentStatusFailed
	fixture.SandboxID = "sandbox-1"
	fixture.Attempt = 2
	created, err := store.CreateDeployment(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendDeploymentEvent(DeploymentEvent{DeploymentID: created.ID, Attempt: 2, EventType: "failure", Stage: "start", Message: "bounded"}); err != nil {
		t.Fatal(err)
	}
	created.SandboxID = ""
	if err := store.RetryDeployment(created, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	queued, err := store.GetDeployment(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queued.Status != DeploymentStatusQueued || queued.SandboxID != "" || queued.Attempt != 2 || !queued.FailedAt.IsZero() {
		t.Fatalf("queued retry = %#v", queued)
	}
	events, more, err := store.ListDeploymentEvents(created.ID, 0, 10)
	if err != nil || more || len(events) != 1 || events[0].EventType != "failure" {
		t.Fatalf("deployment events = %#v more=%v err=%v", events, more, err)
	}
}

func TestDeploymentRetryCreatesImmutableRevisionAndScopesEvents(t *testing.T) {
	store := New(t.TempDir())
	account, _, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "revision-user", OAuthLogin: "revision-user"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateDeployment(deploymentFixture("revision-deployment", account.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendDeploymentEvent(DeploymentEvent{DeploymentID: created.ID, Message: "first", EventType: "output"}); err != nil {
		t.Fatal(err)
	}
	created.Status = DeploymentStatusCompleted
	if err := store.UpdateDeployment(created); err != nil {
		t.Fatal(err)
	}
	created.Version++
	if err := store.RetryDeployment(created, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetDeployment(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.RevisionNumber != 2 {
		t.Fatalf("revision number = %d, want 2", current.RevisionNumber)
	}
	if err := store.AppendDeploymentEvent(DeploymentEvent{DeploymentID: current.ID, Message: "second", EventType: "output"}); err != nil {
		t.Fatal(err)
	}
	revisions, more, err := store.ListDeploymentRevisions(current.ID, 0, 10)
	if err != nil || more || len(revisions) != 2 {
		t.Fatalf("revisions=%#v more=%v err=%v", revisions, more, err)
	}
	first, _, err := store.ListDeploymentRevisionEvents(current.ID, 1, 0, 0, 20)
	if err != nil || len(first) == 0 || first[len(first)-1].Message != "first" {
		t.Fatalf("first revision events=%#v err=%v", first, err)
	}
	second, _, err := store.ListDeploymentRevisionEvents(current.ID, 2, 0, 0, 20)
	if err != nil || len(second) == 0 || second[len(second)-1].Message != "second" {
		t.Fatalf("second revision events=%#v err=%v", second, err)
	}
	if revisions[1].Status != DeploymentStatusCompleted || revisions[0].Status != DeploymentStatusQueued || revisions[1].SpecHash != created.SpecHash || revisions[1].CommitSHA != created.CommitSHA {
		t.Fatalf("immutable history lost: %#v", revisions)
	}
	if err := store.AppendDeploymentEvent(DeploymentEvent{DeploymentID: current.ID, RevisionNumber: 1, Message: "stale output", EventType: "output"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale output error = %v", err)
	}
	if err := store.RetryDeployment(created, time.Now().Add(time.Hour)); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale retry error = %v", err)
	}
	page, more, err := store.ListDeploymentRevisions(current.ID, 2, 1)
	if err != nil || more || len(page) != 1 || page[0].RevisionNumber != 1 {
		t.Fatalf("revision cursor=%#v more=%v err=%v", page, more, err)
	}
}

func TestDeploymentRevisionLegacyUpgradePreservesHistory(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	d, err := store.CreateDeployment(deploymentFixture("legacy-revision", 1))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendDeploymentEvent(DeploymentEvent{DeploymentID: d.ID, EventType: "output", Message: "old output", Attempt: 3}); err != nil {
		t.Fatal(err)
	}
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&deploymentRevisionRecord{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropIndex(&deploymentEventRecord{}, "idx_deployment_events_revision_id"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{"ALTER TABLE deployments DROP COLUMN revision_number", "ALTER TABLE deployment_events DROP COLUMN revision_number"} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := store.migrate(db); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.GetDeployment(d.ID)
	if err != nil || got.CommitSHA != d.CommitSHA || got.SpecJSONEncrypted != d.SpecJSONEncrypted || !got.UpdatedAt.Equal(d.UpdatedAt) || got.RevisionNumber != 1 {
		t.Fatalf("upgrade lost deployment: %#v err=%v", got, err)
	}
	revisions, more, err := store.ListDeploymentRevisions(d.ID, 0, 100)
	if err != nil || more || len(revisions) != 1 {
		t.Fatalf("backfill %#v %v %v", revisions, more, err)
	}
	events, _, err := store.ListDeploymentRevisionEvents(d.ID, 1, 0, 0, 100)
	if err != nil || len(events) != 1 || events[0].Message != "old output" || events[0].Attempt != 3 {
		t.Fatalf("lost legacy output: %#v %v", events, err)
	}
	if !db.Migrator().HasIndex(&deploymentEventRecord{}, "idx_deployment_events_revision_id") {
		t.Fatal("missing revision cursor index")
	}
	var plan []struct{ Detail string }
	if err := db.Raw("EXPLAIN QUERY PLAN SELECT * FROM deployment_events WHERE deployment_id = ? AND revision_number = ? AND id < ? ORDER BY id DESC LIMIT 10", d.ID, 1, 1000).Scan(&plan).Error; err != nil {
		t.Fatal(err)
	}
	for _, line := range plan {
		if strings.Contains(line.Detail, "TEMP B-TREE") {
			t.Fatalf("revision cursor sort: %s", line.Detail)
		}
	}
}

func TestDeploymentRevisionRetryAuditFailureRollsBack(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	d, err := store.CreateDeployment(deploymentFixture("revision-audit", 1))
	if err != nil {
		t.Fatal(err)
	}
	d.Status = DeploymentStatusCompleted
	if err := store.UpdateDeployment(d); err != nil {
		t.Fatal(err)
	}
	d.Version++
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TRIGGER reject_revision_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'audit failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	_, err = store.ApplyMutationWithAudit(AuditEvent{Actor: "test", Action: "deployment.retry", ResourceType: "deployment", ResourceID: d.ID}, func(tx Store) error { return tx.RetryDeployment(d, time.Now().Add(time.Hour)) })
	if !errors.Is(err, ErrAuditEventPersistence) {
		t.Fatalf("audit error = %v", err)
	}
	got, err := store.GetDeployment(d.ID)
	if err != nil || got.RevisionNumber != 1 || got.Status != DeploymentStatusCompleted || got.Version != d.Version {
		t.Fatalf("failed audit modified state: %#v %v", got, err)
	}
	revisions, _, err := store.ListDeploymentRevisions(d.ID, 0, 100)
	if err != nil || len(revisions) != 1 {
		t.Fatalf("failed audit created revision: %#v %v", revisions, err)
	}
}

func TestDeploymentEventsStayWithinCountAndByteBudgets(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	fixture := deploymentFixture("bounded-events", 1)
	created, err := store.CreateDeployment(fixture)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	seed := make([]deploymentEventRecord, maxDeploymentEvents+5)
	for i := range seed {
		seed[i] = deploymentEventRecord{DeploymentID: created.ID, EventType: "output", Message: "x", CreatedAt: time.Now().UTC()}
	}
	if err := db.CreateInBatches(&seed, 500).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.AppendDeploymentEvent(DeploymentEvent{DeploymentID: created.ID, EventType: "output", Message: "z"}); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&deploymentEventRecord{}).Where("deployment_id = ?", created.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != maxDeploymentEvents {
		t.Fatalf("event count = %d, want %d", count, maxDeploymentEvents)
	}
	if err := store.AppendDeploymentEvent(DeploymentEvent{DeploymentID: created.ID, EventType: "output", Message: strings.Repeat("y", maxDeploymentEventBytes)}); err != nil {
		t.Fatal(err)
	}
	var bytes int64
	if err := db.Model(&deploymentEventRecord{}).Where("deployment_id = ?", created.ID).Select("COALESCE(SUM(LENGTH(message)), 0)").Scan(&bytes).Error; err != nil {
		t.Fatal(err)
	}
	if bytes > maxDeploymentEventBytes {
		t.Fatalf("event bytes = %d, want <= %d", bytes, maxDeploymentEventBytes)
	}
	events, _, err := store.ListDeploymentEvents(created.ID, 0, 1)
	if err != nil || len(events) != 1 || len(events[0].Message) != maxDeploymentEventMessage {
		t.Fatalf("latest event = %#v err=%v", events, err)
	}
}

func TestDeploymentScopeQuotaAndRetention(t *testing.T) {
	store := New(t.TempDir())
	account, _, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "quota-user", OAuthLogin: "quota-user"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateDeploymentWithScopeLimit(deploymentFixture("quota-first", account.ID), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDeploymentWithScopeLimit(deploymentFixture("quota-second", account.ID), 1); !errors.Is(err, ErrDeploymentQuotaExceeded) {
		t.Fatalf("second deployment error = %v, want ErrDeploymentQuotaExceeded", err)
	}
	first.Status = DeploymentStatusCompleted
	first.UpdatedAt = time.Now().UTC().Add(-48 * time.Hour)
	if err := store.UpdateDeployment(first); err != nil {
		t.Fatal(err)
	}
	// UpdateDeployment refreshes the timestamp for normal lifecycle writes; set
	// the retention age directly to model an old terminal row.
	db := store.(*DBStore)
	gormDB, err := db.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-48 * time.Hour)
	if err := gormDB.Model(&deploymentRecord{}).Where("id = ?", first.ID).Update("updated_at", old).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.AppendDeploymentEvent(DeploymentEvent{DeploymentID: first.ID, EventType: "lifecycle", Message: "done"}); err != nil {
		t.Fatal(err)
	}
	pruned, err := store.PruneDeployments(time.Now().UTC().Add(-24*time.Hour), 10)
	if err != nil || pruned != 1 {
		t.Fatalf("PruneDeployments() = %d, err=%v", pruned, err)
	}
	if _, err := store.GetDeployment(first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pruned deployment lookup error = %v, want ErrNotFound", err)
	}
	if events, _, err := store.ListDeploymentEvents(first.ID, 0, 10); err != nil || len(events) != 0 {
		t.Fatalf("pruned deployment events = %#v, err=%v", events, err)
	}
}

func TestDeploymentTargetSnapshotSurvivesSQLiteUpgrade(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	fixture := deploymentFixture("old-deployment", 1)
	fixture.SandboxAPIURL, fixture.SandboxAPIKeyEncrypted = "https://region-a.example", "encrypted-key"
	if _, err := store.CreateDeployment(fixture); err != nil {
		t.Fatal(err)
	}
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"sandbox_region", "deployment_template_id", "execution_account_id", "create_request_hash"} {
		if err := db.Migrator().DropColumn(&deploymentRecord{}, field); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.migrate(db); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetDeployment(fixture.ID)
	if err != nil || got.CreateRequestHash != "" || got.ExecutionAccountID != 0 || got.CommitSHA != fixture.CommitSHA || got.SandboxAPIKeyEncrypted != fixture.SandboxAPIKeyEncrypted {
		t.Fatalf("legacy deployment changed: %#v %v", got, err)
	}
	got.SandboxRegion, got.DeploymentTemplateID = "region-a", "deploy-template-a"
	if err := store.UpdateDeployment(got); err != nil {
		t.Fatal(err)
	}
	restored, err := store.GetDeployment(fixture.ID)
	if err != nil || restored.SandboxRegion != got.SandboxRegion || restored.DeploymentTemplateID != got.DeploymentTemplateID {
		t.Fatalf("target snapshot lost: %#v %v", restored, err)
	}
}

func TestDeploymentScopeQuotaSQLBackends(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("dedicated SQL backend gate")
	}
	for _, backend := range []struct{ name, dsn string }{{BackendPostgres, os.Getenv("RUNNERD_POSTGRES_TEST_DSN")}, {BackendMySQL, os.Getenv("RUNNERD_MYSQL_TEST_DSN")}} {
		t.Run(backend.name, func(t *testing.T) {
			store := NewWithOptions(Options{Backend: backend.name, DatabaseDSN: backend.dsn}).(*DBStore)
			db, err := store.dbOrEnsure()
			if err != nil {
				t.Fatal(err)
			}
			requireCatalogMatcherTestDatabase(t, db, backend.name)
			resetSQLBackendTestTables(t, db)
			defer closeTestDB(t, db)
			defer resetSQLBackendTestTables(t, db)
			if err := store.migrate(db); err != nil {
				t.Fatal(err)
			}
			fixtures := make([]Deployment, 2)
			for i := range fixtures {
				account, _, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: fmt.Sprint(i), OAuthLogin: fmt.Sprint(i)}, "user")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.UpsertGitHubInstallation(GitHubInstallation{AccountID: account.ID, InstallationID: 987, AccountType: "organization", AccountLogin: "acme", GitHubAccountID: 600}); err != nil {
					t.Fatal(err)
				}
				fixtures[i] = deploymentFixture(fmt.Sprintf("quota-%d", i), account.ID)
				fixtures[i].ScopeType, fixtures[i].ScopeID = AccountScopeTypeGitHubInstall, 987
				fixtures[i].SandboxRegion, fixtures[i].DeploymentTemplateID = "region-a", "template-a"
			}
			ready := make(chan struct{}, 2)
			release := make(chan struct{})
			results := make(chan error, 2)
			for _, fixture := range fixtures {
				go func(d Deployment) {
					_, err := store.ApplyMutationWithAudit(AuditEvent{Actor: "test", Action: "deployment.create", ResourceType: "deployment", ResourceID: d.ID}, func(tx Store) error {
						if _, err := tx.GetDeployment(d.ID); !errors.Is(err, ErrNotFound) {
							return fmt.Errorf("preflight: %v", err)
						}
						ready <- struct{}{}
						<-release
						_, err := tx.CreateDeploymentWithScopeLimit(d, 1)
						return err
					})
					results <- err
				}(fixture)
			}
			for range fixtures {
				select {
				case <-ready:
				case <-time.After(5 * time.Second):
					t.Fatal("concurrent preflight timed out")
				}
			}
			close(release)
			successes, rejected := 0, 0
			for range fixtures {
				select {
				case err := <-results:
					if err == nil {
						successes++
					} else if errors.Is(err, ErrDeploymentQuotaExceeded) {
						rejected++
					} else {
						t.Errorf("quota mutation: %v", err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("concurrent admission timed out")
				}
			}
			if successes != 1 || rejected != 1 {
				t.Fatalf("admissions=%d rejected=%d", successes, rejected)
			}
			events, err := store.ListAuditEvents(10)
			if err != nil || len(events) != 1 {
				t.Fatalf("audit invariance=%#v %v", events, err)
			}
			deployments, err := store.ListActiveDeployments()
			if err != nil || len(deployments) != 1 || deployments[0].SandboxRegion != "region-a" || deployments[0].DeploymentTemplateID != "template-a" {
				t.Fatalf("target snapshot=%#v %v", deployments, err)
			}
		})
	}
}

func TestDeploymentRevisionsSQLBackends(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("dedicated SQL backend gate")
	}
	for _, backend := range []struct{ name, dsn string }{{BackendPostgres, os.Getenv("RUNNERD_POSTGRES_TEST_DSN")}, {BackendMySQL, os.Getenv("RUNNERD_MYSQL_TEST_DSN")}} {
		t.Run(backend.name, func(t *testing.T) {
			store := NewWithOptions(Options{Backend: backend.name, DatabaseDSN: backend.dsn}).(*DBStore)
			db, err := store.dbOrEnsure()
			if err != nil {
				t.Fatal(err)
			}
			requireCatalogMatcherTestDatabase(t, db, backend.name)
			resetSQLBackendTestTables(t, db)
			defer closeTestDB(t, db)
			defer resetSQLBackendTestTables(t, db)
			if err := store.migrate(db); err != nil {
				t.Fatal(err)
			}
			account, _, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "revision-sql", OAuthLogin: "revision-sql"}, "user")
			if err != nil {
				t.Fatal(err)
			}
			fixture := deploymentFixture("revision-sql", account.ID)
			fixture.ExecutionAccountID = account.ID
			fixture.SourceRepositoryID = 301
			fixture.SourceRepositoryFullName = "contributor/fork"
			fixture.SourceRef = "feature/pr"
			d, err := store.CreateDeploymentWithScopeLimit(fixture, 1)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.AppendDeploymentEvent(DeploymentEvent{DeploymentID: d.ID, EventType: "output", Message: "first"}); err != nil {
				t.Fatal(err)
			}
			d.Status = DeploymentStatusCompleted
			if err := store.UpdateDeployment(d); err != nil {
				t.Fatal(err)
			}
			d.Version++
			d.ExecutionAccountID = account.ID + 1
			_, err = store.ApplyMutationWithAudit(AuditEvent{Actor: "test", Action: "deployment.retry", ResourceType: "deployment", ResourceID: d.ID}, func(tx Store) error {
				if err := tx.RetryDeploymentWithScopeLimit(d, time.Now().Add(time.Hour), 1); err != nil {
					return err
				}
				return tx.AppendDeploymentEvent(DeploymentEvent{DeploymentID: d.ID, EventType: "lifecycle", Message: "second"})
			})
			if err != nil {
				t.Fatal(err)
			}
			got, err := store.GetDeployment(d.ID)
			if err != nil || got.CreateRequestHash != fixture.CreateRequestHash || got.RevisionNumber != 2 || got.ExecutionAccountID != account.ID+1 {
				t.Fatalf("retry %#v %v", got, err)
			}
			revs, more, err := store.ListDeploymentRevisions(d.ID, 0, 10)
			if err != nil || more || len(revs) != 2 || revs[1].ExecutionAccountID != account.ID || revs[0].ExecutionAccountID != account.ID+1 || revs[1].Status != DeploymentStatusCompleted || revs[0].SourceRepositoryID != 301 || revs[1].SourceRepositoryFullName != "contributor/fork" {
				t.Fatalf("revisions %#v %v %v", revs, more, err)
			}
			for number, message := range map[int64]string{1: "first", 2: "second"} {
				events, _, err := store.ListDeploymentRevisionEvents(d.ID, number, 0, 0, 10)
				if err != nil || len(events) != 1 || events[0].Message != message {
					t.Fatalf("revision %d events %#v %v", number, events, err)
				}
			}
			if err := store.migrate(db); err != nil {
				t.Fatal(err)
			}
			revs, _, err = store.ListDeploymentRevisions(d.ID, 0, 10)
			if err != nil || len(revs) != 2 {
				t.Fatalf("restart changed history %#v %v", revs, err)
			}
		})
	}
}

func TestDeploymentContextListsMatchHistoricalRevisionsBeforePagination(t *testing.T) {
	store := New(t.TempDir())
	for i := range 3 {
		fixture := deploymentFixture(fmt.Sprintf("context-%d", i), 1)
		fixture.Ref = "refs/heads/feature/context"
		if i == 2 {
			fixture.AccountID = 2
		}
		d, err := store.CreateDeployment(fixture)
		if err != nil {
			t.Fatal(err)
		}
		d.Status = DeploymentStatusCompleted
		if err := store.UpdateDeployment(d); err != nil {
			t.Fatal(err)
		}
		d, err = store.GetDeployment(d.ID)
		if err != nil {
			t.Fatal(err)
		}
		d.CommitSHA = strings.Repeat("b", 40)
		if err := store.RetryDeployment(d, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	options := DeploymentListOptions{RepositoryFullName: "owner/project", Ref: "refs/heads/feature/context", CommitSHA: strings.Repeat("a", 40), Limit: 1, Offset: 1}
	items, total, err := store.ListDeploymentsForAccount(1, options)
	if err != nil || total != 2 || len(items) != 1 {
		t.Fatalf("items=%v total=%d err=%v", items, total, err)
	}
	matched := items[0].ContextRevision
	if matched == nil || matched.RevisionNumber != 1 || matched.CommitSHA != options.CommitSHA || items[0].RevisionNumber != 2 {
		t.Fatalf("lost historical selection: %#v", items[0])
	}
	options.Ref = "refs/tags/feature/context"
	items, total, err = store.ListDeploymentsForAccount(1, options)
	if err != nil || total != 0 || len(items) != 0 {
		t.Fatalf("mixed branch and tag: %v %d %v", items, total, err)
	}
}

func TestDeploymentSourceSnapshotUpgradeAndImmutableRetry(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	fixture := deploymentFixture("source-upgrade", 1)
	fixture.SourceRepositoryID = 301
	fixture.SourceRepositoryFullName = "contributor/fork"
	fixture.SourceRef = "feature/pr"
	d, err := store.CreateDeployment(fixture)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.GetDeploymentRevision(d.ID, 1)
	if err != nil || first.SourceRepositoryID != 301 || first.SourceRepositoryFullName != fixture.SourceRepositoryFullName {
		t.Fatalf("source %#v %v", first, err)
	}
	d.Status = DeploymentStatusCompleted
	if err := store.UpdateDeployment(d); err != nil {
		t.Fatal(err)
	}
	d, err = store.GetDeployment(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	d.SourceRepositoryID = 302
	d.SourceRepositoryFullName = "new/fork"
	d.SourceRef = "new"
	if err := store.RetryDeployment(d, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	first, err = store.GetDeploymentRevision(d.ID, 1)
	if err != nil || first.SourceRepositoryID != 301 {
		t.Fatal("old source snapshot mutated")
	}
	second, err := store.GetDeploymentRevision(d.ID, 2)
	if err != nil || second.SourceRepositoryID != 302 {
		t.Fatal("new source snapshot missing")
	}
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"deployments", "deployment_revisions"} {
		for _, column := range []string{"source_repository_id", "source_repository_full_name", "source_ref"} {
			if err := db.Exec("ALTER TABLE " + table + " DROP COLUMN " + column).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	for range 2 {
		if err := store.migrate(db); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.GetDeploymentRevision(d.ID, 1)
	if err != nil || got.CommitSHA != first.CommitSHA || got.SpecJSONEncrypted != first.SpecJSONEncrypted || got.SourceRepositoryID != 0 || !got.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("legacy preservation %#v %v", got, err)
	}
}

func TestDeploymentRevisionExecutionActorSurvivesUpgradeAndRetry(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	d, err := store.CreateDeployment(deploymentFixture("actor-history", 1))
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []any{&deploymentRecord{}, &deploymentRevisionRecord{}} {
		if err := db.Migrator().DropColumn(model, "execution_account_id"); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := store.migrate(db); err != nil {
			t.Fatal(err)
		}
	}
	d, err = store.GetDeployment(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.ExecutionAccountID != 0 || d.AccountID != 1 || d.SpecJSONEncrypted != "encrypted-spec" {
		t.Fatal("legacy actor or plan changed")
	}
	d.Status = DeploymentStatusCompleted
	if err := store.UpdateDeployment(d); err != nil {
		t.Fatal(err)
	}
	d.Version++
	d.ExecutionAccountID = 2
	if err := store.RetryDeployment(d, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetDeployment(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccountID != 1 || got.ExecutionAccountID != 2 || got.RevisionNumber != 2 {
		t.Fatal("retry lost creator/actor")
	}
	for number, actor := range map[int64]int64{1: 0, 2: 2} {
		rev, err := store.GetDeploymentRevision(d.ID, number)
		if err != nil {
			t.Fatal(err)
		}
		if rev.ExecutionAccountID != actor {
			t.Fatalf("revision %d actor = %d", number, rev.ExecutionAccountID)
		}
	}
	// Authorization precedes count/pagination, independent of creator identity.
	for _, identity := range []struct {
		id           string
		installation int64
		repo         string
	}{{"shared", 101, "OWNER/PROJECT"}, {"other-install", 102, "owner/project"}, {"other-repo", 101, "owner/hidden"}} {
		fixture := deploymentFixture(identity.id, 2)
		fixture.GitHubInstallationID = identity.installation
		fixture.RepositoryFullName = identity.repo
		if _, err := store.CreateDeployment(fixture); err != nil {
			t.Fatal(err)
		}
	}
	items, total, err := store.ListAuthorizedDeployments([]GitHubInstallationRepositoryAccess{{InstallationID: 101, Repositories: []string{"owner/project"}}}, DeploymentListOptions{Limit: 1, Offset: 1})
	if err != nil || total != 2 || len(items) != 1 || items[0].ID != d.ID {
		t.Fatalf("shared pagination total=%d items=%v err=%v", total, len(items), err)
	}
}
