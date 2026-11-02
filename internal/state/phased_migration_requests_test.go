package state

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestPhasedMigrationDeletedRequestBaselineSQLBackends(t *testing.T) {
	for _, timing := range []string{"resume", "between_pages", "rollback"} {
		t.Run(timing, func(t *testing.T) {
			f := newPhasedTestFixture(t)
			const oldID = "r-0000"
			const newID = "r-0300-replacement"
			jobID := int64(9001)
			for i := 0; i < 520; i++ {
				row := phasedTestRequest(fmt.Sprintf("r-%04d", i))
				if i == 0 {
					row.WorkflowJobID = &jobID
				}
				if err := f.source.Create(&row).Error; err != nil {
					t.Fatal(err)
				}
			}
			f.run(t, "prepare")
			replace := func() {
				t.Helper()
				if err := f.source.Delete(&runnerRequestRecord{}, "id = ?", oldID).Error; err != nil {
					t.Fatal(err)
				}
				row := phasedTestRequest(newID)
				row.WorkflowJobID = &jobID
				if err := f.source.Create(&row).Error; err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			options := f.options
			options.Mode = "sync"
			reached := false
			options.Progress = func(p OfflineMigrationProgress) {
				if p.Stage != "copy and verify request baseline" || p.Rows != 256 || reached {
					return
				}
				reached = true
				if timing != "between_pages" {
					cancel()
				} else {
					replace()
				}
			}
			_, err := MigrateSQLiteToPostgresPhased(ctx, options)
			if !reached {
				t.Fatalf("first page was not committed: %v", err)
			}
			if timing != "between_pages" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("interrupt: %v", err)
				}
				replace()
				if timing == "rollback" {
					if err := f.target.Exec(`CREATE FUNCTION reject_request_batch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected insert failure'; END $$`).Error; err != nil {
						t.Fatal(err)
					}
					if err := f.target.Exec(`CREATE TRIGGER reject_request_batch BEFORE INSERT ON runner_requests FOR EACH ROW WHEN (NEW.id = 'r-0300-replacement') EXECUTE FUNCTION reject_request_batch()`).Error; err != nil {
						t.Fatal(err)
					}
					phasedAssertError(t, f, "sync", "copy and verify request baseline")
					var old runnerRequestRecord
					if err := f.target.First(&old, "id = ?", oldID).Error; err != nil {
						t.Fatal("failed batch lost old target row:", err)
					}
					var checkpoint phasedCheckpoint
					if err := f.target.First(&checkpoint, "table_name = ?", "runner_requests").Error; err != nil {
						t.Fatal(err)
					}
					if checkpoint.Rows != 256 || checkpoint.Done {
						t.Fatalf("failed batch advanced checkpoint: %+v", checkpoint)
					}
					if err := f.target.Exec("DROP TRIGGER reject_request_batch ON runner_requests").Error; err != nil {
						t.Fatal(err)
					}
					if err := f.target.Exec("DROP FUNCTION reject_request_batch()").Error; err != nil {
						t.Fatal(err)
					}
				}
				f.run(t, "sync")
			} else if err != nil {
				t.Fatalf("copy after concurrent replacement: %v", err)
			}
			f.run(t, "sync") // an additional retry must remain idempotent
			inventory, _ := newMigrationInventory()
			table, _ := migrationTableByName(inventory, "runner_requests")
			if _, err := verifyOfflineTable(f.source, f.target, table); err != nil {
				t.Fatal(err)
			}
			var got runnerRequestRecord
			if err := f.target.First(&got, "workflow_job_id = ?", jobID).Error; err != nil {
				t.Fatal(err)
			}
			if got.ID != newID {
				t.Fatalf("job owner=%q", got.ID)
			}
			// The early conflict cleanup must not suppress subsequent source recreation.
			recreated := phasedTestRequest(oldID)
			if err := f.source.Create(&recreated).Error; err != nil {
				t.Fatal(err)
			}
			f.run(t, "sync")
			if _, err := verifyOfflineTable(f.source, f.target, table); err != nil {
				t.Fatal(err)
			}
			report := f.run(t, "cutover")
			if !report.ReadyForPG {
				t.Fatalf("cutover incomplete: %+v", report)
			}
		})
	}
}

func TestPhasedMigrationDeletedRequestReplaySQLBackends(t *testing.T) {
	f := newPhasedTestFixture(t)
	oldJob, newJob := int64(9001), int64(9002)
	old, replacement := phasedTestRequest("old"), phasedTestRequest("replacement")
	old.WorkflowJobID, replacement.WorkflowJobID = &oldJob, &newJob
	for _, row := range []runnerRequestRecord{old, replacement} {
		if err := f.source.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	f.run(t, "prepare")
	f.run(t, "sync")
	// The first replay page reads the replacement's latest state, while the
	// deletion freeing its Job ID is more than one journal page ahead.
	for i := 0; i < 150; i++ {
		if err := f.source.Model(&runnerRequestRecord{}).Where("id = ?", replacement.ID).Update("retry_count", i).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{old.ID, replacement.ID} {
		if err := f.source.Delete(&runnerRequestRecord{}, "id = ?", id).Error; err != nil {
			t.Fatal(err)
		}
	}
	replacement.WorkflowJobID = &oldJob
	if err := f.source.Create(&replacement).Error; err != nil {
		t.Fatal(err)
	}
	f.run(t, "sync")
	f.run(t, "sync")
	inventory, _ := newMigrationInventory()
	table, _ := migrationTableByName(inventory, "runner_requests")
	if _, err := verifyOfflineTable(f.source, f.target, table); err != nil {
		t.Fatal(err)
	}
	if report := f.run(t, "cutover"); !report.ReadyForPG {
		t.Fatalf("cutover incomplete: %+v", report)
	}
}
