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

const (
	accountWorkerLock   int64 = 0x61636374727074
	accountWorkerLimits       = "SELECT set_config('lock_timeout', '2s', true), set_config('max_parallel_workers_per_gather', '0', true)"
)

// Each replica checks the shared publication. Contention and a recent
// publication are normal outcomes, so only actual refresh failures are logged.
func RunAccountReportWorker(ctx context.Context, opts Options, onError func(error)) {
	for ctx.Err() == nil {
		if err := refreshAccountReports(ctx, opts, false, true); err != nil && ctx.Err() == nil {
			onError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

// RefreshAccountReports explicitly requests a refresh or full rebuild.
func RefreshAccountReports(ctx context.Context, opts Options, rebuild bool) error {
	return refreshAccountReports(ctx, opts, rebuild, false)
}

func refreshAccountReports(ctx context.Context, opts Options, rebuild, scheduled bool) error {
	if opts.Backend != BackendPostgres {
		return errors.New("published account reports require PostgreSQL")
	}
	// Coordinator, source snapshot and batch writer use a private pool.
	opts.PostgresMaxOpenConns = 3
	s := &DBStore{opts: opts}
	db, err := s.openWithContext(ctx)
	if err != nil {
		return err
	}
	pool, err := db.DB()
	if err != nil {
		return err
	}
	defer pool.Close()
	db = db.WithContext(ctx)
	return db.Transaction(func(leader *gorm.DB) error {
		var locked bool
		if err := leader.Raw("SELECT pg_try_advisory_xact_lock(?)", accountWorkerLock).Scan(&locked).Error; err != nil {
			return err
		}
		if !locked {
			if scheduled {
				return nil
			}
			return errors.New("account report worker already running")
		}
		if err := leader.Exec(accountWorkerLimits).Error; err != nil {
			return err
		}
		writer := accountBatchWriter{db: db, leader: leader}
		var pub *accountReportPublication
		err := writer.batch(func(tx *gorm.DB) error {
			if err := s.validateAndMigrate(tx, false); err != nil {
				return err
			}
			var err error
			pub, err = accountPublicationForRefresh(tx, scheduled)
			return err
		})
		if err != nil || pub == nil {
			return err
		}
		return refreshAccountSlot(ctx, leader, writer, *pub, rebuild)
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}

func accountPublicationForRefresh(tx *gorm.DB, scheduled bool) (*accountReportPublication, error) {
	exists, err := accountPublicationExists(tx)
	if err != nil {
		return nil, err
	}
	var pub accountReportPublication
	if exists {
		err := tx.Select("definition", "pending_definition").Take(&pub, 1).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	if pub.Definition > accountReportDefinition || pub.PendingDefinition > accountReportDefinition {
		if scheduled {
			return nil, nil // An upgraded replica owns this definition.
		}
		return nil, errors.New("account statistics require a newer worker")
	}
	if pub.Definition != accountReportDefinition && pub.PendingDefinition != accountReportDefinition {
		if err := tx.AutoMigrate(&accountReportPublication{}, &accountFact{}, &accountDay{}, &accountDirtyDay{}, &accountDefaultDashboard{}); err != nil {
			return nil, err
		}
		pub.ID = 1
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&pub).Error; err != nil {
			return nil, err
		}
	}
	q := tx
	if scheduled {
		q = q.Where("definition <> ? OR pending_epoch <> 0 OR published_at IS NULL OR published_at <= statement_timestamp() - interval '5 minutes'", accountReportDefinition)
	}
	err = q.Take(&pub, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &pub, err
}

// Batches commit independently so a replacement worker can resume them. The
// batch lock drains an in-flight write before takeover. Checking the original
// coordinator transaction under that lock fences writes after connection loss.
// Both locks are transaction-scoped, including through a transaction pooler.
type accountBatchWriter struct {
	db     *gorm.DB
	leader *gorm.DB
}

func (w accountBatchWriter) batch(fn func(*gorm.DB) error) error {
	return w.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(accountWorkerLimits).Error; err != nil {
			return err
		}
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", accountWorkerLock+1).Error; err != nil {
			return err
		}
		// Probe the pinned leader transaction after acquiring the batch lock.
		// A successor must acquire that same batch lock before inspecting the slot.
		if err := w.leader.Exec("SELECT 1").Error; err != nil {
			return fmt.Errorf("check account report writer transaction: %w", err)
		}
		return fn(tx)
	})
}

func refreshAccountSlot(ctx context.Context, leader *gorm.DB, writer accountBatchWriter, pub accountReportPublication, rebuild bool) error {
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
		if err := writer.batch(func(tx *gorm.DB) error {
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
	observed, _, err := syncAccountFacts(ctx, writer, slot, epoch, false)
	if err != nil {
		return err
	}
	var sourceCount int64
	if bulk {
		// Pick up commits made during the backfill and count source rows at that snapshot.
		observed, sourceCount, err = syncAccountFacts(ctx, writer, slot, epoch, true)
		if err != nil {
			return err
		}
	}
	if err := refreshAccountDays(leader, writer, slot); err != nil {
		return err
	}
	if bulk {
		if err := verifyAccountRebuild(leader, slot, sourceCount); err != nil {
			return err
		}
	}
	if err := prepareAccountDashboard(leader, slot, observed); err != nil {
		return err
	}
	return leader.Model(&pub).Updates(map[string]any{"slot": slot, "observed_at": observed, "published_at": gorm.Expr("clock_timestamp()"), "epoch": epoch, "inactive_epoch": pub.Epoch, "pending_epoch": 0, "pending_definition": 0, "definition": accountReportDefinition}).Error
}

func syncAccountFacts(ctx context.Context, writer accountBatchWriter, slot int, epoch int64, countSource bool) (time.Time, int64, error) {
	var observed time.Time
	var count int64
	err := writer.db.WithContext(ctx).Transaction(func(src *gorm.DB) error {
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

func writeChangedAccountFacts(src *gorm.DB, writer accountBatchWriter, slot int, epoch int64, observed time.Time) error {
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
		err := writer.batch(func(tx *gorm.DB) error {
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

func removeMissingAccountFacts(src *gorm.DB, writer accountBatchWriter, slot int) error {
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
		if err := writer.batch(func(tx *gorm.DB) error {
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

func refreshAccountDays(leader *gorm.DB, writer accountBatchWriter, slot int) error {
	columns, err := reportMetricColumns(leader)
	if err != nil {
		return err
	}
	additive := additiveReportColumns(columns)
	for {
		var dirty accountDirtyDay
		err := leader.Where("slot = ?", slot).Order("day").Take(&dirty).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			break
		}
		if err != nil {
			return err
		}
		if err := writer.batch(func(tx *gorm.DB) error {
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
