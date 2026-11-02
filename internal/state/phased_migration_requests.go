package state

import "gorm.io/gorm"

// phasedWriteRequests reconciles pending deletes that block a unique Job ID.
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
			var present []string
			if err := reader.Table(table.Name).Where("id IN ?", candidates).Pluck("id", &present).Error; err != nil {
				return err
			}
			retained := make(map[string]bool, len(present))
			for _, id := range present {
				retained[id] = true
			}
			var deleted []string
			for _, id := range candidates {
				if !retained[id] {
					deleted = append(deleted, id)
				}
			}
			// Do not remove a source row that still exists, or advance the journal
			// cursor here. Later replay must still observe unrelated changes and any
			// recreation committed after this snapshot. Cleanup rolls back with a
			// failed insert/verification/checkpoint in the enclosing target transaction.
			if len(deleted) > 0 {
				if err := tx.Table(table.Name).Where("id IN ?", deleted).Delete(map[string]any{}).Error; err != nil {
					return err
				}
			}
		}
	}
	return phasedWriteAndVerify(tx, table, rows, true)
}
