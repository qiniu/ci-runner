package state

import (
	"bytes"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const phasedBatchSize = 256

func (m *phasedMigration) checkpoint(table string) (phasedCheckpoint, error) {
	var checkpoint phasedCheckpoint
	err := m.target.First(&checkpoint, "table_name = ?", table).Error
	return checkpoint, err
}

// The exact selected source rows are compared before the transaction and its
// checkpoint commit. Concurrent source changes are covered by the key journal.
func phasedWriteAndVerify(tx *gorm.DB, table migrationTable, rows []migrationRow, update bool) error {
	if len(rows) == 0 {
		return nil
	}
	page := make([]map[string]any, len(rows))
	for i, row := range rows {
		page[i] = map[string]any(row)
	}
	conflict := clause.OnConflict{DoNothing: true}
	for _, key := range migrationKeyColumns(table) {
		conflict.Columns = append(conflict.Columns, clause.Column{Name: key.Name})
	}
	if update {
		conflict.DoNothing = false
		conflict.DoUpdates = clause.AssignmentColumns(columnNames(table))
	}
	if err := tx.Table(table.Name).Clauses(conflict).Create(page).Error; err != nil {
		return err
	}
	others, err := readMigrationRowsByKeys(tx, table, rows)
	if err != nil {
		return err
	}
	if len(others) != len(rows) {
		return fmt.Errorf("%w: verified batch row count differs in %s", errMigrationPrecondition, table.Name)
	}
	for _, row := range rows {
		key, err := migrationKeyString(table, row)
		if err != nil {
			return err
		}
		other, ok := others[key]
		if !ok {
			return fmt.Errorf("%w: verified batch key differs in %s", errMigrationPrecondition, table.Name)
		}
		expected, err := canonicalMigrationRow(table, row)
		if err != nil {
			return err
		}
		actual, err := canonicalMigrationRow(table, other)
		if err != nil {
			return err
		}
		if !bytes.Equal(expected, actual) {
			return fmt.Errorf("%w: verified batch content differs in %s", errMigrationPrecondition, table.Name)
		}
	}
	return nil
}

func (m *phasedMigration) syncSmallTables() error {
	for _, table := range m.inventory.Tables {
		if table.Name == "runner_requests" || table.Name == "runner_events" {
			continue
		}
		stage := "refresh and verify table " + table.Name
		m.progress(stage, 0)
		// A read transaction gives this table a stable snapshot without blocking WAL
		// writers. Full replacement propagates deletions as well as updates.
		err := m.source.Transaction(func(reader *gorm.DB) error {
			return m.copyTransaction(func(tx *gorm.DB) error {
				if err := tx.Exec("DELETE FROM " + migrationQuoted(table.Name)).Error; err != nil {
					return err
				}
				var copied int64
				after := ""
				for {
					rows, more, err := readMigrationRows(reader, table, after, phasedBatchSize)
					if err != nil {
						return err
					}
					if err := phasedWriteAndVerify(tx, table, rows, false); err != nil {
						return err
					}
					copied += int64(len(rows))
					m.progress(stage, copied)
					if !more {
						break
					}
					after, err = encodeMigrationKey(table, rows[len(rows)-1])
					if err != nil {
						return err
					}
				}
				var count int64
				if err := tx.Table(table.Name).Count(&count).Error; err != nil {
					return err
				}
				if count != copied {
					return fmt.Errorf("%w: refreshed table count differs", errMigrationPrecondition)
				}
				return nil
			})
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *phasedMigration) copyRequests() error {
	table, _ := migrationTableByName(m.inventory, "runner_requests")
	checkpoint, err := m.checkpoint(table.Name)
	if err != nil {
		return err
	}
	stage := "copy and verify request baseline"
	m.progress(stage, checkpoint.Rows)
	if err := m.pauseBatch(); err != nil {
		return err
	}
	for !checkpoint.Done {
		next := checkpoint
		// Read the page and check stale target job owners in the same source
		// snapshot. A delete/recreate between those reads must not be mistaken
		// for an already-replayed deletion.
		err := m.source.Transaction(func(reader *gorm.DB) error {
			rows, more, err := readMigrationRows(reader.Where("id COLLATE BINARY <= ?", m.sourceState.RequestUpper), table, checkpoint.Cursor, phasedBatchSize)
			if err != nil {
				return err
			}
			next.Rows += int64(len(rows))
			next.Done = !more
			if len(rows) != 0 {
				next.Cursor, err = encodeMigrationKey(table, rows[len(rows)-1])
				if err != nil {
					return err
				}
			}
			return m.copyTransaction(func(tx *gorm.DB) error {
				if err := phasedWriteRequests(reader, tx, table, rows); err != nil {
					return err
				}
				return tx.Save(&next).Error
			})
		})
		if err != nil {
			return err
		}

		checkpoint = next
		m.progress(stage, checkpoint.Rows)
		if err := m.pauseBatch(); err != nil {
			return err
		}
	}
	return nil
}

func (m *phasedMigration) replayRequests() error {
	table, _ := migrationTableByName(m.inventory, "runner_requests")
	var upper int64
	if err := m.source.Raw("SELECT COALESCE(MAX(seq), 0) FROM runnerd_transfer_changes").Scan(&upper).Error; err != nil {
		return err
	}
	stage := "replay and verify changed requests"
	var processed int64
	m.progress(stage, 0)
	for m.targetState.JournalCursor < upper {
		var last int64
		var batchCount int
		err := m.source.Transaction(func(reader *gorm.DB) error {
			var changes []struct {
				Seq       int64
				RequestID string
			}
			if err := reader.Table("runnerd_transfer_changes").Where("seq > ? AND seq <= ?", m.targetState.JournalCursor, upper).Order("seq").Limit(phasedBatchSize).Find(&changes).Error; err != nil {
				return err
			}
			if len(changes) == 0 {
				return fmt.Errorf("%w: missing source change journal", errMigrationPrecondition)
			}
			last = changes[len(changes)-1].Seq
			batchCount = len(changes)
			keys := make([]migrationRow, 0, len(changes))
			seen := map[string]bool{}
			for _, change := range changes {
				if !seen[change.RequestID] {
					seen[change.RequestID] = true
					keys = append(keys, migrationRow{"id": change.RequestID})
				}
			}
			current, err := readMigrationRowsByKeys(reader, table, keys)
			if err != nil {
				return err
			}
			rows := make([]migrationRow, 0, len(current))
			var deleted []string
			for _, key := range keys {
				encoded, err := migrationKeyString(table, key)
				if err != nil {
					return err
				}
				if row, ok := current[encoded]; ok {
					rows = append(rows, row)
				} else {
					deleted = append(deleted, key["id"].(string))
				}
			}
			return m.copyTransaction(func(tx *gorm.DB) error {
				if len(deleted) > 0 {
					if err := tx.Table(table.Name).Where("id IN ?", deleted).Delete(map[string]any{}).Error; err != nil {
						return err
					}
					var remaining int64
					if err := tx.Table(table.Name).Where("id IN ?", deleted).Count(&remaining).Error; err != nil {
						return err
					}
					if remaining != 0 {
						return fmt.Errorf("%w: deleted requests remain in target", errMigrationPrecondition)
					}
				}
				if err := phasedWriteRequests(reader, tx, table, rows); err != nil {
					return err
				}
				return tx.Model(&phasedTargetRecord{}).Where("id = 1").Update("journal_cursor", last).Error
			})
		})
		if err != nil {
			return err
		}
		m.targetState.JournalCursor = last
		processed += int64(batchCount)
		m.progress(stage, processed)
		if err := m.pauseBatch(); err != nil {
			return err
		}
	}
	return nil
}

func (m *phasedMigration) copyEvents(final bool) error {
	table, _ := migrationTableByName(m.inventory, "runner_events")
	checkpoint, err := m.checkpoint(table.Name)
	if err != nil {
		return err
	}
	upper := m.targetState.EventWatermark
	if !final {
		if err := m.source.Table(table.Name).Select("COALESCE(MAX(id),0)").Scan(&upper).Error; err != nil {
			return err
		}
	}
	stage := "append and verify event history"
	m.progress(stage, checkpoint.Rows)
	if err := m.pauseBatch(); err != nil {
		return err
	}
	for {
		rows, more, err := readMigrationRows(m.source.Where("id <= ?", upper), table, checkpoint.Cursor, phasedBatchSize)
		if err != nil {
			return err
		}
		next := checkpoint
		next.Rows += int64(len(rows))
		next.Done = final && !more
		if len(rows) > 0 {
			next.Cursor, err = encodeMigrationKey(table, rows[len(rows)-1])
			if err != nil {
				return err
			}
		}
		write := func(tx *gorm.DB) error {
			if err := phasedWriteAndVerify(tx, table, rows, false); err != nil {
				return err
			}
			return tx.Save(&next).Error
		}
		if final {
			// No updates, sequence changes or request writes after PG owns the service.
			err = m.target.Transaction(write)
		} else {
			err = m.copyTransaction(write)
		}
		if err != nil {
			return err
		}
		checkpoint = next
		m.progress(stage, checkpoint.Rows)
		if err := m.pauseBatch(); err != nil {
			return err
		}
		if !more {
			break
		}
	}
	if final {
		var sourceCount, targetCount int64
		if err := m.source.Table(table.Name).Where("id <= ?", upper).Count(&sourceCount).Error; err != nil {
			return err
		}
		if err := m.target.Table(table.Name).Where("id <= ?", upper).Count(&targetCount).Error; err != nil {
			return err
		}
		if sourceCount != checkpoint.Rows || targetCount != checkpoint.Rows {
			return fmt.Errorf("%w: historical event prefix count differs", errMigrationPrecondition)
		}
		if err := m.target.Model(&phasedTargetRecord{}).Where("id = 1").Update("phase", "complete").Error; err != nil {
			return err
		}
		m.targetState.Phase = "complete"
	}
	return nil
}

func (m *phasedMigration) pauseBatch() error {
	if m.options.BatchPause == 0 || m.options.Mode == "cutover" {
		return nil
	}
	timer := time.NewTimer(m.options.BatchPause)
	defer timer.Stop()
	select {
	case <-m.source.Statement.Context.Done():
		return m.source.Statement.Context.Err()
	case <-timer.C:
		return nil
	}
}
