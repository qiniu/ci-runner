package state

import "gorm.io/gorm"

// phasedWriteRequests reconciles stale target owners that block a unique Job ID.
// reader must be the same source read transaction that supplied rows; tx must
// be the copying-only target transaction that also saves the batch checkpoint.
func phasedWriteRequests(reader, tx *gorm.DB, table migrationTable, rows []migrationRow) error {
	owners := make(map[int64]string, len(rows))
	var jobs []int64
	for _, row := range rows {
		if job, ok := row["workflow_job_id"].(int64); ok {
			owners[job] = row["id"].(string)
			jobs = append(jobs, job)
		}
	}
	if len(jobs) > 0 {
		var existing []struct {
			ID            string
			WorkflowJobID int64
		}
		if err := tx.Table(table.Name).Select("id", "workflow_job_id").Where("workflow_job_id IN ?", jobs).Find(&existing).Error; err != nil {
			return err
		}
		var candidates []string
		for _, old := range existing {
			if owners[old.WorkflowJobID] != old.ID {
				candidates = append(candidates, old.ID)
			}
		}
		if len(candidates) > 0 {
			var current []struct {
				ID            string
				WorkflowJobID *int64
			}
			if err := reader.Table(table.Name).Select("id", "workflow_job_id").Where("id IN ?", candidates).Find(&current).Error; err != nil {
				return err
			}
			sourceOwners := make(map[int64]string, len(current))
			for _, row := range current {
				if row.WorkflowJobID != nil {
					sourceOwners[*row.WorkflowJobID] = row.ID
				}
			}
			var deleted []string
			for _, old := range existing {
				if owners[old.WorkflowJobID] != old.ID && sourceOwners[old.WorkflowJobID] != old.ID {
					deleted = append(deleted, old.ID)
				}
			}
			// A recreated ID may exist without still owning the conflicting Job.
			// Remove only stale target owners; leave the journal cursor unchanged
			// so replay restores recreated rows and observes unrelated changes.
			// Cleanup rolls back with any failed insert, verification or checkpoint
			// in the enclosing target transaction.
			if len(deleted) > 0 {
				if err := tx.Table(table.Name).Where("id IN ?", deleted).Delete(map[string]any{}).Error; err != nil {
					return err
				}
			}
		}
	}
	return phasedWriteAndVerify(tx, table, rows, true)
}
