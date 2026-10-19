package state

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestDeploymentContextRevisionsUseBoundedQueries(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	testDeploymentContextRevisionQueries(t, store)
}

func testDeploymentContextRevisionQueries(t *testing.T, store *DBStore) {
	t.Helper()
	for i := range 12 {
		fixture := deploymentFixture(fmt.Sprintf("batch-%02d", i), 1)
		d, err := store.CreateDeployment(fixture)
		if err != nil {
			t.Fatal(err)
		}
		for number := range 2 {
			d.Status = DeploymentStatusCompleted
			if err := store.UpdateDeployment(d); err != nil {
				t.Fatal(err)
			}
			d, err = store.GetDeployment(d.ID)
			if err != nil {
				t.Fatal(err)
			}
			if number == 1 {
				d.Ref, d.CommitSHA = "refs/heads/other", strings.Repeat("b", 40)
			}
			if err := store.RetryDeployment(d, time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			d, err = store.GetDeployment(d.ID)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	queries := 0
	if err := db.Callback().Query().Before("gorm:query").Register("test:deployment-queries", func(tx *gorm.DB) {
		if !tx.DryRun {
			queries++
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Query().Remove("test:deployment-queries")
	for _, limit := range []int{1, 10} {
		queries = 0
		items, total, err := store.ListDeploymentsForAccount(1, DeploymentListOptions{Ref: "main", CommitSHA: strings.Repeat("a", 40), Limit: limit, Offset: 1})
		if err != nil || total != 12 || len(items) != limit {
			t.Fatalf("list: len=%d total=%d err=%v", len(items), total, err)
		}
		if queries != 3 {
			t.Fatalf("page size %d used %d queries; want count, page and one revision batch", limit, queries)
		}
		for _, item := range items {
			if item.ContextRevision == nil || item.ContextRevision.RevisionNumber != 2 || item.RevisionNumber != 3 {
				t.Fatalf("lost newest matching history: %#v", item)
			}
		}
	}
}

func TestDeploymentEventBudgetUsesCountersAndLegacyBootstrap(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	testDeploymentEventBudgetQueries(t, store)
}

func testDeploymentEventBudgetQueries(t *testing.T, store *DBStore) {
	t.Helper()
	d, err := store.CreateDeployment(deploymentFixture("unicode-budget", 1))
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.GetDeploymentRevision(d.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	message := strings.Repeat("界", maxDeploymentEventMessage/3)
	seed := make([]deploymentEventRecord, 150)
	for i := range seed {
		seed[i] = deploymentEventRecord{DeploymentID: d.ID, RevisionNumber: 1, EventType: "output", Message: message, CreatedAt: time.Now().UTC()}
	}
	if err := db.CreateInBatches(&seed, 50).Error; err != nil {
		t.Fatal(err)
	}
	aggregates := 0
	if err := db.Callback().Row().After("gorm:row").Register("test:budget-aggregates", func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "SUM(") {
			aggregates++
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Row().Remove("test:budget-aggregates")
	if err := store.AppendDeploymentEvent(DeploymentEvent{DeploymentID: d.ID, EventType: "output", Message: "latest"}); err != nil {
		t.Fatal(err)
	}
	if aggregates != 1 {
		t.Fatalf("append aggregated %d times, want once even when evicting many rows", aggregates)
	}
	var workers sync.WaitGroup
	errors := make(chan error, 16)
	for range 16 {
		workers.Go(func() {
			errors <- store.AppendDeploymentEvent(DeploymentEvent{DeploymentID: d.ID, EventType: "output", Message: "latest"})
		})
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if aggregates != 1 {
		t.Fatalf("subsequent appends rescanned the event history %d times", aggregates-1)
	}
	events, more, err := store.ListDeploymentRevisionEvents(d.ID, 1, 0, 0, 500)
	if err != nil || more || len(events) == 0 || events[len(events)-1].Message != "latest" {
		t.Fatalf("events=%d more=%v err=%v", len(events), more, err)
	}
	bytes := 0
	for _, event := range events {
		bytes += len(event.Message)
	}
	if bytes > maxDeploymentEventBytes || bytes+len(message) <= maxDeploymentEventBytes {
		t.Fatalf("retained %d bytes; want maximum fitting history within %d bytes", bytes, maxDeploymentEventBytes)
	}
	var revision deploymentRevisionRecord
	if err := db.Where("deployment_id = ? AND number = 1", d.ID).First(&revision).Error; err != nil {
		t.Fatal(err)
	}
	if revision.EventCount != int64(len(events)) || revision.EventBytes != int64(bytes) || !revision.EventBudgetInitialized || !revision.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("event counters/lifecycle timestamp disagree: %#v", revision)
	}
}

func TestDeploymentQuerySQLBackends(t *testing.T) {
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
			testDeploymentContextRevisionQueries(t, store)
			testDeploymentEventBudgetQueries(t, store)
		})
	}
}
