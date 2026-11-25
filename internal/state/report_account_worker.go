package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const accountWorkerLock int64 = 0x61636374727074

// RefreshAccountReports runs in a separate process with its own two database
// connections. A failed or interrupted run leaves the published slot readable.
func RefreshAccountReports(ctx context.Context, opts Options, rebuild bool) error {
	if opts.Backend != BackendPostgres {
		return errors.New("published account reports require PostgreSQL")
	}
	opts.PostgresMaxOpenConns = 2
	s := &DBStore{opts: opts}
	db, err := s.open()
	if err != nil {
		return err
	}
	pool, err := db.DB()
	if err != nil {
		return err
	}
	defer pool.Close()
	return db.WithContext(ctx).Connection(func(writer *gorm.DB) error {
		writer = writer.Session(&gorm.Session{NewDB: true})
		var locked bool
		if err := writer.Raw("SELECT pg_try_advisory_lock(?)", accountWorkerLock).Scan(&locked).Error; err != nil {
			return err
		}
		if !locked {
			return errors.New("account report worker already running")
		}
		if err := writer.Exec("SET lock_timeout = '2s'").Error; err != nil {
			return err
		}
		if err := writer.Exec("SET max_parallel_workers_per_gather = 0").Error; err != nil {
			return err
		}
		// Closing this private pool also releases the session-level writer lock.
		if err := s.validateAndMigrate(writer, false); err != nil {
			return err
		}
		if err := writer.AutoMigrate(&accountReportPublication{}, &accountFact{}, &accountDay{}, &accountDirtyDay{}, &accountDefaultDashboard{}); err != nil {
			return err
		}
		return refreshAccountSlot(ctx, db, writer, rebuild)
	})
}

func refreshAccountSlot(ctx context.Context, db, writer *gorm.DB, rebuild bool) error {
	pub := accountReportPublication{ID: 1}
	if err := writer.Clauses(clause.OnConflict{DoNothing: true}).Create(&pub).Error; err != nil {
		return err
	}
	if err := writer.First(&pub, 1).Error; err != nil {
		return err
	}
	if pub.Definition > accountReportDefinition || pub.PendingDefinition > accountReportDefinition {
		return errors.New("account statistics require a newer worker")
	}
	slot := 1 - pub.Slot
	// Keep the requested epoch until publication. Retrying after a crash
	// reuses committed fact batches. The other slot must also adopt this
	// epoch before it can be published again.
	epoch := pub.PendingEpoch
	if epoch == 0 {
		epoch = max(pub.Epoch, 1)
	}
	if (rebuild && epoch <= pub.Epoch) || (pub.Definition != accountReportDefinition && pub.PendingDefinition != accountReportDefinition) {
		epoch = max(pub.Epoch, pub.PendingEpoch) + 1
	}
	bulk := pub.InactiveEpoch != epoch
	if pub.PendingEpoch != epoch {
		if err := writer.Transaction(func(tx *gorm.DB) error {
			if bulk {
				if err := tx.Exec("INSERT INTO report_account_dirty_days (slot,day) SELECT DISTINCT slot,day FROM report_account_days WHERE slot=? ON CONFLICT DO NOTHING", slot).Error; err != nil {
					return err
				}
			}
			return tx.Model(&pub).Updates(map[string]any{"pending_epoch": epoch, "pending_definition": accountReportDefinition}).Error
		}); err != nil {
			return err
		}
	}
	observed, _, err := syncAccountFacts(ctx, db, writer, slot, epoch, false)
	if err != nil {
		return err
	}
	var sourceCount int64
	if bulk {
		// Pick up commits made during the backfill and count source rows at that snapshot.
		observed, sourceCount, err = syncAccountFacts(ctx, db, writer, slot, epoch, true)
		if err != nil {
			return err
		}
	}
	if err := refreshAccountDays(writer, slot); err != nil {
		return err
	}
	if bulk {
		if err := verifyAccountRebuild(writer, slot, sourceCount); err != nil {
			return err
		}
	}
	if err := prepareAccountDashboard(writer, slot, observed); err != nil {
		return err
	}
	return writer.Model(&pub).Updates(map[string]any{"slot": slot, "observed_at": observed, "epoch": epoch, "inactive_epoch": pub.Epoch, "pending_epoch": 0, "pending_definition": 0, "definition": accountReportDefinition}).Error
}

func syncAccountFacts(ctx context.Context, db, writer *gorm.DB, slot int, epoch int64, countSource bool) (time.Time, int64, error) {
	var observed time.Time
	var count int64
	err := db.WithContext(ctx).Transaction(func(src *gorm.DB) error {
		if err := src.Exec("SET LOCAL max_parallel_workers_per_gather = 0").Error; err != nil {
			return err
		}
		if err := src.Raw("SELECT statement_timestamp()").Scan(&observed).Error; err != nil {
			return err
		}
		if countSource {
			if err := src.Table("runner_requests").Count(&count).Error; err != nil {
				return err
			}
		}
		if err := writeChangedAccountFacts(src, writer, slot, epoch, observed); err != nil {
			return err
		}
		return removeMissingAccountFacts(src, writer, slot)
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("sync account report facts: %w", err)
	}
	return observed.UTC(), count, nil
}

func markAccountDays(tx *gorm.DB, slot int, days map[time.Time]bool) error {
	dirty := make([]accountDirtyDay, 0, len(days))
	for day := range days {
		dirty = append(dirty, accountDirtyDay{slot, day})
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&dirty).Error
}

// An offline rebuild verifies the independent source count and reconciles its
// daily rollups with the newly built facts before changing the published slot.
func verifyAccountRebuild(db *gorm.DB, slot int, sourceCount int64) error {
	type totals struct {
		Requests, Samples int64
		Seconds           float64
	}
	read := func(table string) (totals, error) {
		var t totals
		err := db.Table(table).Select("coalesce(sum(requests),0) AS requests, coalesce(sum(runtime_samples),0) AS samples, coalesce(sum(runtime_seconds),0) AS seconds").Where("slot=?", slot).Scan(&t).Error
		return t, err
	}
	f, err := read((accountFact{}).TableName())
	if err != nil {
		return err
	}
	d, err := read((accountDay{}).TableName())
	if err != nil {
		return err
	}
	if f.Requests != sourceCount || f.Requests != d.Requests || f.Samples != d.Samples || math.Abs(f.Seconds-d.Seconds) > 1e-9*max(1, f.Seconds) {
		return fmt.Errorf("account rebuild reconciliation failed: source rows %d, facts %+v, days %+v", sourceCount, f, d)
	}
	return nil
}

const accountFactBatchSize = 256

func writeChangedAccountFacts(src, writer *gorm.DB, slot int, epoch int64, observed time.Time) error {
	base, err := reportBase(src, RunnerReportQuery{})
	if err != nil {
		return err
	}
	// PostgreSQL's binary record encoding preserves field types, nulls and exact
	// timestamps independently of session timezone. Hash the shared projection:
	// versionless corrections and owner changes count, unrelated lease writes do not.
	const signature = "sha256(record_send(p))"
	query := src.Table("(?) AS p", base.Select(reportProjectionFields)).
		Joins("LEFT JOIN report_account_facts AS f ON f.slot = ? AND f.id = p.id", slot).
		Where("f.epoch IS DISTINCT FROM ? OR f.fingerprint IS DISTINCT FROM "+signature+" OR f.recheck_at <= ?", epoch, observed).
		Select("p.*, " + signature + " AS fingerprint, f.queued_at AS old_queue")

	rows, err := query.Rows()
	if err != nil {
		return err
	}
	defer rows.Close()

	facts := make([]accountFact, 0, accountFactBatchSize)
	days := map[time.Time]bool{}
	flush := func() error {
		if len(facts) == 0 {
			return nil
		}
		err := writer.Transaction(func(tx *gorm.DB) error {
			if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&facts).Error; err != nil {
				return err
			}
			return markAccountDays(tx, slot, days)
		})
		facts = facts[:0]
		clear(days)
		return err
	}
	for rows.Next() {
		var source factSource
		if err := src.ScanRows(rows, &source); err != nil {
			return err
		}
		f := source.fact(slot, observed)
		f.Epoch = epoch
		facts = append(facts, f)
		days[f.QueuedAt.UTC().Truncate(24*time.Hour)] = true
		if source.OldQueue != nil {
			days[source.OldQueue.UTC().Truncate(24*time.Hour)] = true
		}
		if len(facts) == accountFactBatchSize {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return flush()
}

func removeMissingAccountFacts(src, writer *gorm.DB, slot int) error {
	days := map[time.Time]bool{}
	var orphan []accountFact
	for cursor := ""; ; {
		q := src.Table("report_account_facts AS f").Select("f.id, f.queued_at").Joins("LEFT JOIN runner_requests AS r ON r.id=f.id").Where("f.slot=? AND r.id IS NULL", slot)
		if cursor != "" {
			q = q.Where("f.id > ?", cursor)
		}
		if err := q.Order("f.id").Limit(accountFactBatchSize).Find(&orphan).Error; err != nil {
			return err
		}
		if len(orphan) == 0 {
			return nil
		}
		ids := make([]string, 0, len(orphan))
		for _, f := range orphan {
			ids = append(ids, f.ID)
			days[f.QueuedAt.UTC().Truncate(24*time.Hour)] = true
		}
		if err := writer.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("slot=? AND id IN ?", slot, ids).Delete(&accountFact{}).Error; err != nil {
				return err
			}
			return markAccountDays(tx, slot, days)
		}); err != nil {
			return err
		}
		clear(days)
		cursor = orphan[len(orphan)-1].ID
	}
}

func refreshAccountDays(writer *gorm.DB, slot int) error {
	columns, err := reportMetricColumns(writer)
	if err != nil {
		return err
	}
	additive := additiveReportColumns(columns)
	for {
		var dirty accountDirtyDay
		err := writer.Where("slot = ?", slot).Order("day").Take(&dirty).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			break
		}
		if err != nil {
			return err
		}
		if err := writer.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("slot = ? AND day = ?", slot, dirty.Day).Delete(&accountDay{}).Error; err != nil {
				return err
			}
			stmt := "INSERT INTO report_account_days (slot, day, last_queued_at, " + accountDims + ", " + strings.Join(additive, ",") + ", active_accounts, active_repositories) " +
				"SELECT slot, ?, max(queued_at), " + accountTextDimensions() + ", " + sumReportColumns(additive) + ", max(active_accounts), max(active_repositories) " +
				"FROM report_account_facts WHERE slot = ? AND queued_at >= ? AND queued_at < ? GROUP BY slot, " + accountTextDimensions()
			if err := tx.Exec(stmt, dirty.Day, slot, dirty.Day, dirty.Day.Add(24*time.Hour)).Error; err != nil {
				return err
			}
			return tx.Delete(&dirty).Error
		}); err != nil {
			return err
		}
	}
	return nil
}
