package state

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAdminRunnerReadsCancelWhileWaitingForConnection(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	// Occupy SQLite's only connection, so these reads can only finish by cancellation.
	conn, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, operation := range []string{"list", "metrics"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				if operation == "list" {
					_, _, err := store.ListStatesCursor(ctx, RunnerRequestListOptions{Limit: 100}, nil)
					done <- err
				} else {
					_, err := store.GetRunnerRequestMetrics(ctx)
					done <- err
				}
			}()
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("query error=%v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancelled read kept waiting for the occupied connection")
			}
		})
	}
}
