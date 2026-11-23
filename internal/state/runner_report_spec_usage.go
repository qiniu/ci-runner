package state

import (
	"sort"

	"gorm.io/gorm"
)

// Read both catalogs on the cohort's report connection and transaction. Only
// stable identity and display fields belong in this projection.
func readReportSpecCatalog(db *gorm.DB, spec string) ([]ReportSpecUsage, error) {
	global := db.Table("runner_profiles").Select("name, 'global' AS source, '' AS scope_type, 0 AS scope_id, enabled, published, template_source")
	scoped := db.Table("scoped_runner_profiles").Select("name, 'scoped_custom' AS source, scope_type, scope_id, enabled, FALSE AS published, '' AS template_source")
	query := db.Table("(? UNION ALL ?) AS catalog", global, scoped)
	if spec != "" {
		id, err := decodeReportSpec(spec)
		if err != nil {
			return nil, err
		}
		query = query.Where("name = ? AND source = ? AND scope_type = ? AND scope_id = ?", reportTextKey(db, id.Name), reportTextKey(db, id.Source), reportTextKey(db, id.ScopeType), id.ScopeID)
	}
	var rows []struct {
		Name, Source, ScopeType, TemplateSource string
		ScopeID                                 int64
		Enabled, Published                      bool
	}
	if err := query.Limit(ReportMaxRows + 1).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) > ReportMaxRows {
		return nil, ErrReportTooManyGroups
	}
	catalog := make([]ReportSpecUsage, 0, len(rows))
	for _, r := range rows {
		catalog = append(catalog, ReportSpecUsage{Name: r.Name, Source: r.Source, ScopeType: r.ScopeType, ScopeID: r.ScopeID, Enabled: r.Enabled, Published: r.Published, TemplateSource: r.TemplateSource})
	}
	return catalog, nil
}

// A segment retains its stable group key even when two display labels coincide.
type ReportSpecSegment struct {
	Started  int64          `json:"started,omitempty"`
	Key      string         `json:"key"`
	Label    string         `json:"label"`
	Requests int64          `json:"requests"`
	Runtime  ReportDuration `json:"runtime"`
	Other    bool           `json:"other,omitempty"`
}
type ReportSpecMix struct {
	Requests []ReportSpecSegment `json:"requests"`
	Runtime  []ReportSpecSegment `json:"runtime"`
}

type specBreakdown struct {
	repos, owners, trend []ReportSpecSegment
	outcomes             map[string]int64
}

func (b *specBreakdown) addGroup(dimension string, group ReportSpecSegment) {
	switch dimension {
	case "spec_repo":
		b.repos = append(b.repos, group)
	case "spec_owner":
		b.owners = append(b.owners, group)
	case "spec_trend":
		b.trend = append(b.trend, group)
	case "spec_result":
		if b.outcomes == nil {
			b.outcomes = make(map[string]int64)
		}
		b.outcomes[group.Key] = group.Requests
	}
}

func (r *RunnerReport) specBreakdowns() map[string]*specBreakdown {
	if r.groupTotals != nil {
		return r.specGroups
	}
	type groupKey struct{ spec, dimension, key string }
	counts := make(map[groupKey]*ReportSpecSegment)
	for _, row := range r.Rows {
		bucket := reportBucketDate(row.QueuedAt, r.Window.Location, r.Query.Grain)
		add := func(dim, key, label string) {
			k := groupKey{row.Spec, dim, key}
			g := counts[k]
			if g == nil {
				g = &ReportSpecSegment{Key: key, Label: label}
				counts[k] = g
			}
			if label < g.Label {
				g.Label = label
			}
			g.Requests++
			if row.started {
				g.Started++
			}
			addReportDuration(&g.Runtime, row.terminal && row.started, row.RuntimeSeconds)
		}
		add("spec_repo", row.Repository, row.Repository)
		add("spec_owner", row.Owner, row.OwnerLabel)
		add("spec_trend", bucket, bucket)
	}
	groups := make(map[string]*specBreakdown)
	for key, item := range counts {
		if groups[key.spec] == nil {
			groups[key.spec] = &specBreakdown{}
		}
		groups[key.spec].addGroup(key.dimension, *item)
	}
	return groups
}

func rankSpecGroups(rows []ReportSpecSegment) *ReportSpecMix {
	rank := func(runtime bool) []ReportSpecSegment {
		sort.Slice(rows, func(i, j int) bool {
			a, b := rows[i], rows[j]
			if runtime {
				if a.Runtime.Seconds != b.Runtime.Seconds {
					return a.Runtime.Seconds > b.Runtime.Seconds
				}
				if (a.Runtime.Samples > 0) != (b.Runtime.Samples > 0) {
					return a.Runtime.Samples > 0
				}
			}
			if a.Requests != b.Requests {
				return a.Requests > b.Requests
			}
			return a.Key < b.Key
		})
		n := min(5, len(rows))
		result := append([]ReportSpecSegment{}, rows[:n]...)
		if n < len(rows) {
			other := ReportSpecSegment{Other: true}
			for _, r := range rows[n:] {
				other.Requests += r.Requests
				other.Started += r.Started
				other.Runtime.Seconds += r.Runtime.Seconds
				other.Runtime.Samples += r.Runtime.Samples
				other.Runtime.Eligible += r.Runtime.Eligible
			}
			result = append(result, other)
		}
		return result
	}
	return &ReportSpecMix{Requests: rank(false), Runtime: rank(true)}
}

// Rank the whole cohort independently of spec pages and per-spec Top 5 cutoffs.
func (r *RunnerReport) specUsageSummary() *ReportSpecUsage {
	mix := func(dimension string) *ReportSpecMix {
		groups := r.reportGroups(dimension)
		rows := make([]ReportSpecSegment, 0, len(groups))
		for _, g := range groups {
			rows = append(rows, ReportSpecSegment{Key: g.Key, Label: g.Label, Requests: g.Requests, Runtime: g.Runtime, Started: g.Started})
		}
		return rankSpecGroups(rows)
	}
	t := r.Totals
	summary := &ReportSpecUsage{
		Requests: t.Requests, Started: t.Started, Repositories: t.ActiveRepositories,
		Runtime: t.Runtime, Creation: t.Creation,
		AverageRuntimeSeconds: reportMean(t.Runtime), AverageCreationSeconds: reportMean(t.Creation),
		RepositoryMix: mix("repository"), Owners: mix("owner"), JobResults: make(map[string]int64),
	}
	for _, spec := range r.SpecItems {
		for result, count := range spec.JobResults {
			summary.JobResults[result] += count
		}
	}
	var runtime []float64
	for _, row := range r.Rows {
		if row.RuntimeSeconds != nil {
			runtime = append(runtime, *row.RuntimeSeconds)
		}
	}
	summary.P95RuntimeSeconds = reportP95(runtime)
	return summary
}
