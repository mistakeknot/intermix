package eval

import (
	"fmt"
	"sort"
	"strings"
)

// Report holds aggregated statistics for a matrix evaluation run.
type Report struct {
	TotalCells      int
	SuccessCount    int
	FailureCount    int
	PassRate        float64
	ByRepo          map[string]*RepoStats
	ByTask          map[string]*TaskStats
	ByOutcome       map[string]int
	FailureClusters []FailureCluster
	Delta           *DeltaReport
}

// RepoStats tracks per-repo pass/fail counts.
type RepoStats struct {
	Total, Success, Failure int
}

// TaskStats tracks per-task pass/fail counts.
type TaskStats struct {
	Total, Success, Failure int
}

// FailureCluster groups cells that share the same non-success outcome.
type FailureCluster struct {
	Outcome string
	Count   int
	Cells   []string // "repo:task"
	BeadID  string
}

// DeltaReport compares two evaluation segments to surface regressions and fixes.
type DeltaReport struct {
	Fixed, Regressed, Stable, NewCells int
	FixedCells, RegressedCells         []string
}

// GenerateReport builds a Report from cell results. If prevResults is non-nil,
// a delta comparison is included.
func GenerateReport(results []CellResult, prevResults []CellResult) *Report {
	r := &Report{
		ByRepo:    make(map[string]*RepoStats),
		ByTask:    make(map[string]*TaskStats),
		ByOutcome: make(map[string]int),
	}

	for _, cr := range results {
		r.TotalCells++
		r.ByOutcome[cr.Outcome]++

		// Per-repo stats.
		rs, ok := r.ByRepo[cr.Repo]
		if !ok {
			rs = &RepoStats{}
			r.ByRepo[cr.Repo] = rs
		}
		rs.Total++

		// Per-task stats.
		ts, ok := r.ByTask[cr.Task]
		if !ok {
			ts = &TaskStats{}
			r.ByTask[cr.Task] = ts
		}
		ts.Total++

		if cr.Outcome == OutcomeSuccess {
			r.SuccessCount++
			rs.Success++
			ts.Success++
		} else if cr.Outcome != OutcomeSkipped {
			r.FailureCount++
			rs.Failure++
			ts.Failure++
		}
	}

	if r.TotalCells > 0 {
		r.PassRate = float64(r.SuccessCount) / float64(r.TotalCells) * 100
	}

	r.FailureClusters = ClusterFailures(results)

	if prevResults != nil {
		r.Delta = CompareSegments(prevResults, results)
	}

	return r
}

// ClusterFailures groups non-success, non-skipped results by outcome, sorted by
// count descending.
func ClusterFailures(results []CellResult) []FailureCluster {
	groups := make(map[string][]string)
	for _, cr := range results {
		if cr.Outcome == OutcomeSuccess || cr.Outcome == OutcomeSkipped {
			continue
		}
		key := cr.Repo + ":" + cr.Task
		groups[cr.Outcome] = append(groups[cr.Outcome], key)
	}

	clusters := make([]FailureCluster, 0, len(groups))
	for outcome, cells := range groups {
		clusters = append(clusters, FailureCluster{
			Outcome: outcome,
			Count:   len(cells),
			Cells:   cells,
		})
	}

	sort.Slice(clusters, func(i, j int) bool {
		return clusters[i].Count > clusters[j].Count
	})

	return clusters
}

// CompareSegments computes the delta between a previous and current evaluation
// run. It identifies fixed cells (was_fail -> now_pass), regressed cells
// (was_pass -> now_fail), stable cells, and new cells not present in prev.
func CompareSegments(prev, curr []CellResult) *DeltaReport {
	d := &DeltaReport{}

	prevByKey := make(map[string]string, len(prev))
	for _, cr := range prev {
		prevByKey[cr.Repo+":"+cr.Task] = cr.Outcome
	}

	for _, cr := range curr {
		key := cr.Repo + ":" + cr.Task
		prevOutcome, existed := prevByKey[key]
		if !existed {
			d.NewCells++
			continue
		}

		prevPass := prevOutcome == OutcomeSuccess
		currPass := cr.Outcome == OutcomeSuccess

		switch {
		case !prevPass && currPass:
			d.Fixed++
			d.FixedCells = append(d.FixedCells, key)
		case prevPass && !currPass:
			d.Regressed++
			d.RegressedCells = append(d.RegressedCells, key)
		default:
			d.Stable++
		}
	}

	return d
}

// FormatReport renders a Report as markdown-formatted text.
func FormatReport(r *Report) string {
	var b strings.Builder

	// Summary stats.
	b.WriteString("# Evaluation Report\n\n")
	b.WriteString("## Summary\n\n")
	b.WriteString(fmt.Sprintf("- **Total cells:** %d\n", r.TotalCells))
	b.WriteString(fmt.Sprintf("- **Success:** %d\n", r.SuccessCount))
	b.WriteString(fmt.Sprintf("- **Failures:** %d\n", r.FailureCount))
	b.WriteString(fmt.Sprintf("- **Pass rate:** %.1f%%\n", r.PassRate))

	// Outcome distribution.
	if len(r.ByOutcome) > 0 {
		b.WriteString("\n## Outcome Distribution\n\n")
		outcomes := make([]string, 0, len(r.ByOutcome))
		for o := range r.ByOutcome {
			outcomes = append(outcomes, o)
		}
		sort.Strings(outcomes)
		for _, o := range outcomes {
			b.WriteString(fmt.Sprintf("- %s: %d\n", o, r.ByOutcome[o]))
		}
	}

	// Per-repo breakdown.
	if len(r.ByRepo) > 0 {
		b.WriteString("\n## By Repository\n\n")
		repos := make([]string, 0, len(r.ByRepo))
		for name := range r.ByRepo {
			repos = append(repos, name)
		}
		sort.Strings(repos)
		for _, name := range repos {
			rs := r.ByRepo[name]
			b.WriteString(fmt.Sprintf("- **%s**: %d/%d passed\n", name, rs.Success, rs.Total))
		}
	}

	// Per-task breakdown.
	if len(r.ByTask) > 0 {
		b.WriteString("\n## By Task\n\n")
		tasks := make([]string, 0, len(r.ByTask))
		for name := range r.ByTask {
			tasks = append(tasks, name)
		}
		sort.Strings(tasks)
		for _, name := range tasks {
			ts := r.ByTask[name]
			b.WriteString(fmt.Sprintf("- **%s**: %d/%d passed\n", name, ts.Success, ts.Total))
		}
	}

	// Failure clusters.
	if len(r.FailureClusters) > 0 {
		b.WriteString("\n## Failure Clusters\n\n")
		for _, fc := range r.FailureClusters {
			b.WriteString(fmt.Sprintf("### %s (%d cells)\n\n", fc.Outcome, fc.Count))
			for _, cell := range fc.Cells {
				b.WriteString(fmt.Sprintf("- %s\n", cell))
			}
			b.WriteString("\n")
		}
	}

	// Delta report.
	if r.Delta != nil {
		d := r.Delta
		b.WriteString("## Delta (vs. previous)\n\n")
		b.WriteString(fmt.Sprintf("- **Fixed:** %d\n", d.Fixed))
		b.WriteString(fmt.Sprintf("- **Regressed:** %d\n", d.Regressed))
		b.WriteString(fmt.Sprintf("- **Stable:** %d\n", d.Stable))
		b.WriteString(fmt.Sprintf("- **New cells:** %d\n", d.NewCells))

		if len(d.FixedCells) > 0 {
			b.WriteString("\n**Fixed cells:**\n")
			for _, c := range d.FixedCells {
				b.WriteString(fmt.Sprintf("- %s\n", c))
			}
		}
		if len(d.RegressedCells) > 0 {
			b.WriteString("\n**Regressed cells:**\n")
			for _, c := range d.RegressedCells {
				b.WriteString(fmt.Sprintf("- %s\n", c))
			}
		}
	}

	return b.String()
}
