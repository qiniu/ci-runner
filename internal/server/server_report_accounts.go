package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

// The snapshot carries only the public report projection. Rows are null for
// PostgreSQL aggregate-only cohorts and an empty array for empty detail cohorts.
type sharedAccountSnapshot struct {
	Report  *state.RunnerReport
	Groups  map[string][]state.ReportGroup
	Rows    []state.ReportRequest
	Created time.Time
}

const accountSnapshotDecodeLimit = 128 << 20

func (s *Server) saveAccountSnapshot(ctx context.Context, entry cachedRunnerReport) error {
	data, err := json.Marshal(sharedAccountSnapshot{entry.report, entry.report.GroupTotals, entry.report.Rows, entry.created})
	if err != nil {
		return err
	}
	if len(data) > accountSnapshotDecodeLimit {
		return state.ErrReportSnapshotTooLarge
	}
	var compressed bytes.Buffer
	writer, err := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	if err != nil {
		return err
	}
	if _, err := writer.Write(data); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return s.store.SaveReportSnapshot(ctx, entry.id, entry.created.Add(reportRetention), entry.report.RetainedEntries(), compressed.Bytes())
}

func (s *Server) restoreAccountSnapshot(ctx context.Context, id string) (cachedRunnerReport, error) {
	result := s.reports.group.DoChan("restore:"+id, func() (any, error) {
		readCtx, cancel := context.WithTimeout(context.Background(), reportTimeout)
		defer cancel()
		release, err := s.reports.acquire(readCtx)
		if err != nil {
			return nil, err
		}
		defer release()
		if entry, ok := s.reports.byID(id); ok {
			return entry, nil
		}
		raw, err := s.store.LoadReportSnapshot(readCtx, id, time.Now())
		if err != nil {
			return nil, err
		}
		reader, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		data, err := io.ReadAll(io.LimitReader(reader, accountSnapshotDecodeLimit+1))
		if err != nil {
			return nil, err
		}
		if len(data) > accountSnapshotDecodeLimit {
			return nil, state.ErrReportSnapshotTooLarge
		}
		var saved sharedAccountSnapshot
		if err := json.Unmarshal(data, &saved); err != nil {
			return nil, err
		}
		if saved.Report == nil || saved.Groups == nil {
			return nil, errors.New("invalid stored account report")
		}
		report := saved.Report
		report.Query.IncludeAccountUsage = true
		report.GroupTotals, report.Rows = saved.Groups, saved.Rows
		entry := cachedRunnerReport{id: id, key: reportQueryKey(report.Query), created: saved.Created, report: report, distributions: report.ChartDistributions()}
		if err := readCtx.Err(); err != nil {
			return nil, err
		}
		if time.Since(entry.created) >= reportRetention {
			return nil, state.ErrNotFound
		}
		s.reports.put(entry)
		return entry, nil
	})
	select {
	case <-ctx.Done():
		return cachedRunnerReport{}, ctx.Err()
	case value := <-result:
		if value.Err != nil {
			return cachedRunnerReport{}, value.Err
		}
		return value.Val.(cachedRunnerReport), nil
	}
}
