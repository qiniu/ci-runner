package state

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ReportSnapshotLimit    = 8
	ReportSnapshotEntries  = 2 * ReportMaxRows
	maxReportSnapshotBytes = 16<<20 - 1
	reportSnapshotWriter   = "writer"
)

var ErrReportSnapshotTooLarge = errors.New("encoded report snapshot exceeds the size limit; narrow the date range or filters")

func (s *DBStore) SaveReportSnapshot(ctx context.Context, id string, expires time.Time, entries int, payload []byte) error {
	if len(payload) > maxReportSnapshotBytes {
		return ErrReportSnapshotTooLarge
	}
	if entries > ReportSnapshotEntries {
		return ErrReportTooManyGroups
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// An upsert that updates the same guard obtains a write lock, including
		// on the first concurrent publications. Readers never acquire it.
		guard := reportSpecSnapshotRecord{ID: reportSnapshotWriter, ExpiresAt: expires.UTC()}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{"expires_at"})}).Create(&guard).Error; err != nil {
			return err
		}
		var existing []reportSpecSnapshotRecord
		if err := tx.Select("id", "entries", "expires_at").Where("id <> ?", reportSnapshotWriter).Order("expires_at DESC, id ASC").Find(&existing).Error; err != nil {
			return err
		}
		now := time.Now()
		var remove []string
		count, retained := 1, entries
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
		return tx.Create(&reportSpecSnapshotRecord{ID: id, ExpiresAt: expires.UTC(), Entries: entries, Payload: payload}).Error
	})
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
