package state

import (
	"database/sql"
	"fmt"
	"math"

	"gorm.io/gorm"
)

func (m *phasedMigration) cutover() error {
	if m.targetState.Phase == "ready" || m.targetState.Phase == "complete" {
		return nil
	}
	checkpoint, err := m.checkpoint("runner_requests")
	if err != nil {
		return err
	}
	if !checkpoint.Done {
		return fmt.Errorf("%w: finish request baseline with sync before cutover", errMigrationPrecondition)
	}
	m.progress("seal SQLite source for cutover", 0)
	if err := m.sourceImmediate(func(tx *gorm.DB) error {
		rows, err := m.finalTableRows(tx)
		if err != nil {
			return err
		}
		if rows > m.options.MaxFinalRows {
			return fmt.Errorf("%w: final table row budget exceeded; rehearse and adjust --max-final-rows before cutover", errMigrationPrecondition)
		}
		var pending int64
		if err := tx.Table("runnerd_transfer_changes").Where("seq > ?", m.targetState.JournalCursor).Count(&pending).Error; err != nil {
			return err
		}
		if pending > m.options.MaxFinalChanges {
			return fmt.Errorf("%w: too many final request changes; run sync again before cutover", errMigrationPrecondition)
		}
		return tx.Model(&phasedSourceRecord{}).Where("id = 1").Update("phase", "sealed").Error
	}); err != nil {
		return err
	}
	m.sourceState.Phase = "sealed"
	// A failed final sync leaves the durable source fence in place. resume-source
	// checks that PG has not been released before allowing SQLite writes again.
	if err := m.syncSmallTables(); err != nil {
		return err
	}
	if err := m.replayRequests(); err != nil {
		return err
	}
	m.progress("verify cutover and reserve historical sequences", 0)
	if err := m.copyTransaction(func(tx *gorm.DB) error {
		var sourceCount, targetCount, pending int64
		if err := m.source.Table("runner_requests").Count(&sourceCount).Error; err != nil {
			return err
		}
		if err := tx.Table("runner_requests").Count(&targetCount).Error; err != nil {
			return err
		}
		if err := m.source.Table("runnerd_transfer_changes").Where("seq > ?", m.targetState.JournalCursor).Count(&pending).Error; err != nil {
			return err
		}
		if sourceCount != targetCount || pending != 0 {
			return fmt.Errorf("%w: requests have not converged", errMigrationPrecondition)
		}
		// This runs only before ready is committed. Explicit historical inserts after
		// cutover never change sequences while the service allocates new IDs.
		var eventWatermark int64
		for _, table := range m.inventory.Tables {
			for _, column := range table.Columns {
				if !column.AutoIncrement {
					continue
				}
				watermark, err := migrationSequenceWatermark(m.source, table, column)
				if err != nil {
					return err
				}
				if watermark == math.MaxInt64 {
					return fmt.Errorf("%w: source sequence exhausted", errMigrationPrecondition)
				}
				if table.Name == "runner_events" {
					eventWatermark = watermark
				}
				if watermark > 0 {
					var sequence sql.NullString
					if err := tx.Raw("SELECT pg_get_serial_sequence(?, ?)", table.Name, column.Name).Scan(&sequence).Error; err != nil {
						return err
					}
					if !sequence.Valid {
						return fmt.Errorf("%w: target sequence missing", errMigrationPrecondition)
					}
					if err := tx.Exec("SELECT setval(?::regclass, ?, true)", sequence.String, watermark).Error; err != nil {
						return err
					}
				}
			}
		}
		return tx.Model(&phasedTargetRecord{}).Where("id = 1").Updates(map[string]any{"phase": "ready", "event_watermark": eventWatermark}).Error
	}); err != nil {
		return err
	}
	// Do not infer rollback after an ambiguous commit. A fresh status/resume call
	// observes the durable target phase while holding the same importer lock.
	return m.load()
}

func (m *phasedMigration) resumeSource() error {
	if m.targetState.Phase != "copying" {
		return fmt.Errorf("%w: PG is already released; SQLite cannot resume business writes", errMigrationPrecondition)
	}
	m.progress("restore SQLite source writes before PG release", 0)
	if err := m.sourceImmediate(func(tx *gorm.DB) error {
		return tx.Model(&phasedSourceRecord{}).Where("id = 1").Update("phase", "copying").Error
	}); err != nil {
		return err
	}
	m.sourceState.Phase = "copying"
	return nil
}

func (m *phasedMigration) report() (PhasedMigrationReport, error) {
	report := PhasedMigrationReport{Phase: m.targetState.Phase, SourceSealed: m.sourceState.Phase == "sealed", EventHighWatermark: m.targetState.EventWatermark}
	requests, err := m.checkpoint("runner_requests")
	if err != nil {
		return report, err
	}
	events, err := m.checkpoint("runner_events")
	if err != nil {
		return report, err
	}
	report.RequestsCopied, report.EventsCopied = requests.Rows, events.Rows
	var eventRows int64
	if err := m.source.Table("runner_events").Count(&eventRows).Error; err != nil {
		return report, err
	}
	report.PendingEventRows = max(int64(0), eventRows-events.Rows)
	report.FinalTableRows, err = m.finalTableRows(m.source)
	if err != nil {
		return report, err
	}
	if err := m.source.Table("runnerd_transfer_changes").Where("seq > ?", m.targetState.JournalCursor).Count(&report.PendingRequestChanges).Error; err != nil {
		return report, err
	}
	report.ReadyForPG = report.Phase == "ready" || report.Phase == "complete"
	report.HistoryComplete = report.Phase == "complete"
	report.ReadyForCutover = report.Phase == "copying" && requests.Done && report.PendingRequestChanges <= m.options.MaxFinalChanges && report.FinalTableRows <= m.options.MaxFinalRows
	return report, nil
}

func (m *phasedMigration) finalTableRows(db *gorm.DB) (int64, error) {
	var total int64
	for _, table := range m.inventory.Tables {
		if table.Name == "runner_requests" || table.Name == "runner_events" {
			continue
		}
		var count int64
		if err := db.Table(table.Name).Count(&count).Error; err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}
