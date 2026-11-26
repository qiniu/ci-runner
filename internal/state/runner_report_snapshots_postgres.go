package state

import (
	"fmt"

	"gorm.io/gorm"
)

// Old writers still count every row before inserting a snapshot. Defer their
// live deletes until the insert supplies the new entry count, then apply the
// original eviction order to the legacy pool alone. The shared writer row
// serializes that transaction on both versions.
func installPostgresSpecSnapshotRetention(db *gorm.DB) error {
	var installed bool
	if err := db.Raw(`SELECT EXISTS (SELECT 1 FROM pg_trigger
		WHERE tgrelid = 'report_spec_snapshots'::regclass AND tgname = 'runnerd_prune_report_snapshots')`).Scan(&installed).Error; err != nil {
		return err
	}
	if installed {
		return nil
	}
	if err := db.Exec(fmt.Sprintf(`CREATE OR REPLACE FUNCTION runnerd_report_snapshot_retention() RETURNS trigger
		LANGUAGE plpgsql SET search_path FROM CURRENT AS $body$
		DECLARE
			candidate record;
			kept_count integer := 1;
			kept_entries integer;
		BEGIN
			IF TG_OP = 'DELETE' THEN
				IF OLD.expires_at > statement_timestamp()
					AND (pg_trigger_depth() = 1 OR OLD.id LIKE '%[1]s%%') THEN
					RETURN NULL;
				END IF;
				RETURN OLD;
			END IF;

			kept_entries := NEW.entries;
			FOR candidate IN SELECT id, entries, expires_at FROM report_spec_snapshots
				WHERE id <> NEW.id AND id <> '%[2]s' AND id NOT LIKE '%[1]s%%'
				ORDER BY expires_at DESC, id ASC
			LOOP
				IF candidate.expires_at <= statement_timestamp()
					OR kept_count >= %[3]d OR kept_entries + candidate.entries > %[4]d THEN
					DELETE FROM report_spec_snapshots WHERE id = candidate.id;
				ELSE
					kept_count := kept_count + 1;
					kept_entries := kept_entries + candidate.entries;
				END IF;
			END LOOP;
			RETURN NEW;
		END;
		$body$`, protectedSpecSnapshot, reportSnapshotWriter, ReportSnapshotLimit, ReportSnapshotEntries)).Error; err != nil {
		return err
	}
	if err := db.Exec(`CREATE TRIGGER runnerd_defer_report_snapshot_deletes BEFORE DELETE ON report_spec_snapshots
		FOR EACH ROW EXECUTE FUNCTION runnerd_report_snapshot_retention()`).Error; err != nil {
		return err
	}
	return db.Exec(fmt.Sprintf(`CREATE TRIGGER runnerd_prune_report_snapshots AFTER INSERT ON report_spec_snapshots
		FOR EACH ROW WHEN (NEW.id <> '%s' AND NEW.id NOT LIKE '%s%%') EXECUTE FUNCTION runnerd_report_snapshot_retention()`,
		reportSnapshotWriter, protectedSpecSnapshot)).Error
}
