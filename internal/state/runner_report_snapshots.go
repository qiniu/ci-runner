package state

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ReportSnapshotLimit    = 8
	ReportSnapshotEntries  = 2 * ReportMaxRows
	maxReportSnapshotBytes = 16<<20 - 1
	reportSnapshotWriter   = "writer"
	protectedSpecSnapshot  = "spec-"
)

var ErrReportSnapshotTooLarge = errors.New("encoded report snapshot exceeds the size limit; narrow the date range or filters")

// ErrSpecSnapshotCapacity means admitting a new snapshot would exceed the live spec budget.
var ErrSpecSnapshotCapacity = errors.New("spec report snapshot capacity reached; retry shortly")

// SpecReportSnapshot identifies the saved report and includes the payload when
// an existing snapshot is reused at capacity.
type SpecReportSnapshot struct {
	ID            string
	ReusedPayload []byte
}

func (s *DBStore) SaveSpecReportSnapshot(ctx context.Context, id, queryKey string, expires time.Time, entries int, payload []byte) (SpecReportSnapshot, error) {
	if s.opts.Backend != BackendPostgres {
		err := s.SaveReportSnapshot(ctx, id, expires, entries, payload)
		return SpecReportSnapshot{ID: id}, err
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(queryKey)))
	snapshot := reportSpecSnapshotRecord{
		ID: protectedSpecSnapshot + id, ExpiresAt: expires.UTC(), Entries: entries, Payload: payload, QueryKey: &key,
	}
	var saved SpecReportSnapshot
	err := s.writeReportSnapshot(ctx, snapshot, func(tx *gorm.DB) error {
		var err error
		saved, err = saveProtectedSpecSnapshot(tx, snapshot)
		return err
	})
	return saved, err
}

func (s *DBStore) SaveReportSnapshot(ctx context.Context, id string, expires time.Time, entries int, payload []byte) error {
	snapshot := reportSpecSnapshotRecord{ID: id, ExpiresAt: expires.UTC(), Entries: entries, Payload: payload}
	return s.writeReportSnapshot(ctx, snapshot, func(tx *gorm.DB) error {
		if s.opts.Backend == BackendPostgres {
			// The insert trigger applies the same quota to old and new writers.
			return tx.Create(&snapshot).Error
		}
		return saveEvictableReportSnapshot(tx, snapshot)
	})
}

func (s *DBStore) writeReportSnapshot(ctx context.Context, snapshot reportSpecSnapshotRecord, save func(*gorm.DB) error) error {
	if len(snapshot.Payload) > maxReportSnapshotBytes {
		return ErrReportSnapshotTooLarge
	}
	if snapshot.Entries > ReportSnapshotEntries {
		return ErrReportTooManyGroups
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// An upsert that updates the same guard obtains a write lock, including
		// on the first concurrent publications. Readers never acquire it.
		guard := reportSpecSnapshotRecord{ID: reportSnapshotWriter, ExpiresAt: snapshot.ExpiresAt}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{"expires_at"})}).Create(&guard).Error; err != nil {
			return err
		}
		return save(tx)
	})
}

func saveEvictableReportSnapshot(tx *gorm.DB, snapshot reportSpecSnapshotRecord) error {
	var existing []reportSpecSnapshotRecord
	if err := tx.Select("id", "entries", "expires_at").Where("id <> ?", reportSnapshotWriter).
		Order("expires_at DESC, id ASC").Find(&existing).Error; err != nil {
		return err
	}
	now := time.Now()
	var remove []string
	count, retained := 1, snapshot.Entries
	for _, old := range existing {
		if !old.ExpiresAt.After(now) || count >= ReportSnapshotLimit || old.Entries+retained > ReportSnapshotEntries {
			remove = append(remove, old.ID)
			continue
		}
		count++
		retained += old.Entries
	}
	if len(remove) > 0 {
		if err := tx.Where("id IN ?", remove).Delete(&reportSpecSnapshotRecord{}).Error; err != nil {
			return err
		}
	}
	return tx.Create(&snapshot).Error
}

func saveProtectedSpecSnapshot(tx *gorm.DB, snapshot reportSpecSnapshotRecord) (SpecReportSnapshot, error) {
	if err := tx.Where("id LIKE ? AND expires_at <= statement_timestamp()", protectedSpecSnapshot+"%").
		Delete(&reportSpecSnapshotRecord{}).Error; err != nil {
		return SpecReportSnapshot{}, err
	}
	var used struct{ Count, Entries int }
	if err := tx.Model(&reportSpecSnapshotRecord{}).Where("id LIKE ?", protectedSpecSnapshot+"%").
		Select("count(*) AS count, coalesce(sum(entries), 0) AS entries").Scan(&used).Error; err != nil {
		return SpecReportSnapshot{}, err
	}
	if used.Count < ReportSnapshotLimit && used.Entries+snapshot.Entries <= ReportSnapshotEntries {
		return SpecReportSnapshot{ID: snapshot.ID}, tx.Create(&snapshot).Error
	}
	var existing reportSpecSnapshotRecord
	err := tx.Select("id", "payload").Where("id LIKE ? AND query_key = ? AND expires_at > statement_timestamp()", protectedSpecSnapshot+"%", snapshot.QueryKey).
		Order("expires_at DESC, id ASC").Take(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return SpecReportSnapshot{}, ErrSpecSnapshotCapacity
	}
	return SpecReportSnapshot{ID: existing.ID, ReusedPayload: existing.Payload}, err
}

func (s *DBStore) LoadReportSnapshot(ctx context.Context, id string, now time.Time) ([]byte, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, err
	}
	var row reportSpecSnapshotRecord
	err = db.WithContext(ctx).Select("id", "payload").Where("id = ? AND id <> ? AND expires_at > ?", id, reportSnapshotWriter, now.UTC()).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && row.ID != id) {
		return nil, ErrNotFound
	}
	return row.Payload, err
}
