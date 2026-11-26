package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

// The snapshot carries only the public report projection. Rows are null for
// PostgreSQL aggregate-only cohorts and an empty array for empty detail cohorts.
type sharedUsageSnapshot struct {
	Report  *state.RunnerReport
	Groups  map[string][]state.ReportGroup
	Rows    []state.ReportRequest
	Created time.Time
}

const accountSnapshotDecodeLimit = 128 << 20

func (s *Server) saveUsageSnapshot(ctx context.Context, entry cachedRunnerReport, timing *runnerReportTiming) error {
	start := time.Now()
	payload, err := encodeUsageSnapshot(entry)
	timing.snapshotEncode = time.Since(start)
	if err != nil {
		return err
	}
	start = time.Now()
	err = s.store.SaveReportSnapshot(ctx, entry.id, entry.created.Add(reportRetention), entry.report.RetainedEntries(), payload)
	timing.snapshotWrite = time.Since(start)
	return err
}

func encodeUsageSnapshot(entry cachedRunnerReport) ([]byte, error) {
	data, err := json.Marshal(sharedUsageSnapshot{entry.report, entry.report.GroupTotals, entry.report.Rows, entry.created})
	if err != nil {
		return nil, err
	}
	if len(data) > accountSnapshotDecodeLimit {
		return nil, state.ErrReportSnapshotTooLarge
	}
	var compressed bytes.Buffer
	writer, err := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	if err != nil {
		return nil, err
	}
	if _, err := writer.Write(data); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return compressed.Bytes(), nil
}

func (s *Server) restoreUsageSnapshot(ctx context.Context, id string) (cachedRunnerReport, runnerReportTiming, error) {
	result := s.reports.group.DoChan("restore:"+id, func() (any, error) {
		restore := timedRunnerReport{timing: runnerReportTiming{cache: "snapshot_lookup"}}
		readCtx, cancel := context.WithTimeout(context.Background(), reportTimeout)
		defer cancel()
		start := time.Now()
		release, err := s.reports.acquire(readCtx)
		restore.timing.slotWait = time.Since(start)
		if err != nil {
			return restore, err
		}
		defer release()
		if entry, ok := s.reports.byID(id); ok {
			restore.entry = entry
			restore.timing.cache = "snapshot_hit"
			return restore, nil
		}
		start = time.Now()
		raw, err := s.store.LoadReportSnapshot(readCtx, id, start)
		restore.timing.snapshotLookup = time.Since(start)
		if err != nil {
			return restore, err
		}
		start = time.Now()
		entry, err := decodeUsageSnapshot(id, raw)
		restore.timing.snapshotDecode = time.Since(start)
		if err != nil {
			return restore, err
		}
		start = time.Now()
		entry.distributions = entry.report.ChartDistributions()
		restore.timing.charts = time.Since(start)
		if err := entry.checkRestoreDeadline(readCtx); err != nil {
			return restore, err
		}
		s.reports.put(entry)
		restore.entry = entry
		return restore, nil
	})
	select {
	case <-ctx.Done():
		return cachedRunnerReport{}, runnerReportTiming{}, ctx.Err()
	case value := <-result:
		restore := value.Val.(timedRunnerReport)
		restore.timing.shared = value.Shared
		return restore.entry, restore.timing, value.Err
	}
}

func decodeUsageSnapshot(id string, raw []byte) (cachedRunnerReport, error) {
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return cachedRunnerReport{}, err
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, accountSnapshotDecodeLimit+1))
	if err != nil {
		return cachedRunnerReport{}, err
	}
	if len(data) > accountSnapshotDecodeLimit {
		return cachedRunnerReport{}, state.ErrReportSnapshotTooLarge
	}
	var saved sharedUsageSnapshot
	if err := json.Unmarshal(data, &saved); err != nil {
		return cachedRunnerReport{}, err
	}
	if saved.Report == nil || saved.Groups == nil {
		return cachedRunnerReport{}, errors.New("invalid stored usage report")
	}
	report := saved.Report
	report.Query.IncludeEfficiency = strings.HasPrefix(id, efficiencySnapshotPrefix)
	report.Query.IncludeAccountUsage = !report.Query.IncludeEfficiency
	report.GroupTotals, report.Rows = saved.Groups, saved.Rows
	return cachedRunnerReport{id: id, key: reportQueryKey(report.Query), created: saved.Created, report: report}, nil
}
