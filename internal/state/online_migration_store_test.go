package state

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func migrationTestStore(t *testing.T, role string) *DBStore {
	t.Helper()
	store := New(t.TempDir()).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTestDB(t, db) })
	if err := db.Create(&accountRecord{ID: 1, Role: role}).Error; err != nil {
		t.Fatal(err)
	}
	return store
}

func waitMigrationStoreCondition(t *testing.T, store *onlineMigrationStore, ready func(*onlineMigrationBarrier) bool) {
	t.Helper()
	ctx := migrationTestContext(t)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		store.mu.Lock()
		ok := ready(store.barrier)
		store.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestOnlineMigrationStorePinsWholeAuditAndQueuesLogs(t *testing.T) {
	source := migrationTestStore(t, "user")
	target := migrationTestStore(t, "admin")
	facade, err := newOnlineMigrationStore(source, 1)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	mutationDone := make(chan error, 1)
	go func() {
		_, err := facade.ApplyMutationWithAudit(AuditEvent{Actor: "test", Action: "test.transaction"}, func(tx Store) error {
			_, err := tx.UpsertAccountPreference(AccountPreference{ScopeType: "account", ScopeID: 1, Namespace: "test", Key: "key", ValueJSON: "{}"})
			close(entered)
			<-release
			return err
		})
		mutationDone <- err
	}()
	<-entered
	ctx := migrationTestContext(t)
	barrierResult := make(chan *onlineMigrationBarrier, 1)
	barrierError := make(chan error, 1)
	go func() {
		barrier, err := facade.drain(ctx, 4)
		barrierResult <- barrier
		barrierError <- err
	}()
	waitMigrationStoreCondition(t, facade, func(b *onlineMigrationBarrier) bool { return b != nil })
	accountResult := make(chan Account, 1)
	accountError := make(chan error, 1)
	go func() {
		account, err := facade.GetAccount(1)
		accountResult <- account
		accountError <- err
	}()
	logDone := make(chan struct{})
	go func() {
		facade.AppendStagedLog("runner", "stdout.log", "runner_hook", []byte("queued log"))
		close(logDone)
	}()
	waitMigrationStoreCondition(t, facade, func(b *onlineMigrationBarrier) bool { return b != nil && b.waiting == 2 })
	select {
	case <-barrierResult:
		t.Fatal("barrier passed an unfinished audit transaction")
	case <-logDone:
		t.Fatal("queued log crossed the operation barrier")
	default:
	}
	close(release)
	if err := <-mutationDone; err != nil {
		t.Fatal(err)
	}
	barrier := <-barrierResult
	if err := <-barrierError; err != nil {
		t.Fatal(err)
	}
	defer barrier.abort()
	events, err := source.ListAuditEvents(10)
	if err != nil || len(events) != 1 {
		t.Fatalf("source audit transaction did not finish: %v", err)
	}
	if _, err := source.GetAccountPreference("account", 1, "test", "key"); err != nil {
		t.Fatal(err)
	}
	// Test only the facade mechanics here. Actual protocol publication must
	// additionally establish replicated data, fencing and durable authority.
	if err := barrier.publish(target, 2); err != nil {
		t.Fatal(err)
	}
	if account := <-accountResult; account.Role != "admin" {
		t.Fatal("queued read used the old Store")
	}
	if err := <-accountError; err != nil {
		t.Fatal(err)
	}
	<-logDone
	if log, err := target.ReadLog("runner", "stdout.log", 1024); err != nil || string(log) != "queued log" {
		t.Fatalf("queued log was lost: %v", err)
	}
	if _, err := source.ReadLog("runner", "stdout.log", 1024); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("queued log reached old Store: %v", err)
	}
	if err := barrier.publish(source, 3); !errors.Is(err, errOnlineMigrationNotReady) {
		t.Fatalf("stale guard published a Store: %v", err)
	}
}

func TestOnlineMigrationStoreDrainTimeoutKeepsOriginalEpoch(t *testing.T) {
	source := migrationTestStore(t, "user")
	facade, err := newOnlineMigrationStore(source, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, release, err := facade.pin()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := facade.drain(ctx, 2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("undrained barrier did not time out: %v", err)
	}
	if _, err := facade.drain(context.Background(), 2); !errors.Is(err, errOnlineMigrationNotReady) {
		t.Fatalf("unbounded barrier accepted: %v", err)
	}
	account, err := facade.GetAccount(1)
	if err != nil || account.Role != "user" || facade.epoch != 1 {
		t.Fatalf("timed-out barrier changed authority: %v", err)
	}
}

func TestOnlineMigrationStoreQueueBudgetCancelsWithoutDroppingCalls(t *testing.T) {
	source := migrationTestStore(t, "user")
	facade, err := newOnlineMigrationStore(source, 1)
	if err != nil {
		t.Fatal(err)
	}
	barrier, err := facade.drain(migrationTestContext(t), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.abort()
	if _, err := facade.drain(migrationTestContext(t), 1); !errors.Is(err, errOnlineMigrationNotReady) {
		t.Fatalf("overlapping barrier accepted: %v", err)
	}
	finished := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := facade.GetAccount(1)
			finished <- err
		}()
	}
	select {
	case <-barrier.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("queue saturation did not cancel preparation")
	}
	barrier.abort()
	for range 2 {
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
	}
	if facade.epoch != 1 {
		t.Fatal("queue saturation changed primary epoch")
	}
}
