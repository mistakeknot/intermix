package eval

import "testing"

func TestGenerateReport_BasicStats(t *testing.T) {
	results := []CellResult{
		{Repo: "chi", Task: "add-endpoint", Outcome: OutcomeSuccess},
		{Repo: "cobra", Task: "add-endpoint", Outcome: OutcomeSuccess},
		{Repo: "chi", Task: "fix-bug", Outcome: OutcomeContextLimit},
		{Repo: "cobra", Task: "fix-bug", Outcome: OutcomeContextLimit},
	}

	r := GenerateReport(results, nil)

	if r.TotalCells != 4 {
		t.Errorf("TotalCells = %d, want 4", r.TotalCells)
	}
	if r.SuccessCount != 2 {
		t.Errorf("SuccessCount = %d, want 2", r.SuccessCount)
	}
	if r.FailureCount != 2 {
		t.Errorf("FailureCount = %d, want 2", r.FailureCount)
	}
	if r.PassRate != 50.0 {
		t.Errorf("PassRate = %f, want 50.0", r.PassRate)
	}

	// Should have exactly 1 failure cluster (context_limit with 2 cells).
	if len(r.FailureClusters) != 1 {
		t.Fatalf("FailureClusters count = %d, want 1", len(r.FailureClusters))
	}
	fc := r.FailureClusters[0]
	if fc.Outcome != OutcomeContextLimit {
		t.Errorf("cluster outcome = %q, want %q", fc.Outcome, OutcomeContextLimit)
	}
	if fc.Count != 2 {
		t.Errorf("cluster count = %d, want 2", fc.Count)
	}

	// Per-repo stats.
	chiStats := r.ByRepo["chi"]
	if chiStats == nil {
		t.Fatal("missing repo stats for chi")
	}
	if chiStats.Success != 1 || chiStats.Failure != 1 || chiStats.Total != 2 {
		t.Errorf("chi stats = %+v, want {Total:2 Success:1 Failure:1}", *chiStats)
	}

	// Per-task stats.
	fixBugStats := r.ByTask["fix-bug"]
	if fixBugStats == nil {
		t.Fatal("missing task stats for fix-bug")
	}
	if fixBugStats.Success != 0 || fixBugStats.Failure != 2 {
		t.Errorf("fix-bug stats = %+v, want {Total:2 Success:0 Failure:2}", *fixBugStats)
	}

	// No delta when prevResults is nil.
	if r.Delta != nil {
		t.Error("Delta should be nil when prevResults is nil")
	}

	// FormatReport should not panic and should contain key info.
	text := FormatReport(r)
	if text == "" {
		t.Error("FormatReport returned empty string")
	}
}

func TestCompareSegments(t *testing.T) {
	prev := []CellResult{
		{Repo: "chi", Task: "add-endpoint", Outcome: OutcomeContextLimit},
		{Repo: "cobra", Task: "add-endpoint", Outcome: OutcomeSuccess},
	}
	curr := []CellResult{
		{Repo: "chi", Task: "add-endpoint", Outcome: OutcomeSuccess},
		{Repo: "cobra", Task: "add-endpoint", Outcome: OutcomeContextLimit},
	}

	d := CompareSegments(prev, curr)

	if d.Fixed != 1 {
		t.Errorf("Fixed = %d, want 1", d.Fixed)
	}
	if d.Regressed != 1 {
		t.Errorf("Regressed = %d, want 1", d.Regressed)
	}
	if d.Stable != 0 {
		t.Errorf("Stable = %d, want 0", d.Stable)
	}
	if d.NewCells != 0 {
		t.Errorf("NewCells = %d, want 0", d.NewCells)
	}

	if len(d.FixedCells) != 1 || d.FixedCells[0] != "chi:add-endpoint" {
		t.Errorf("FixedCells = %v, want [chi:add-endpoint]", d.FixedCells)
	}
	if len(d.RegressedCells) != 1 || d.RegressedCells[0] != "cobra:add-endpoint" {
		t.Errorf("RegressedCells = %v, want [cobra:add-endpoint]", d.RegressedCells)
	}
}
