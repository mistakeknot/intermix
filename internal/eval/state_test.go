package eval

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReconstructState_Empty(t *testing.T) {
	// Non-existent file should return empty state, no error.
	s, err := ReconstructState("/tmp/intermix-test-nonexistent-" + t.Name() + ".jsonl")
	if err != nil {
		t.Fatalf("expected nil error for non-existent file, got %v", err)
	}
	if s.CellCount != 0 {
		t.Errorf("expected CellCount=0, got %d", s.CellCount)
	}
	if s.SegmentID != 0 {
		t.Errorf("expected SegmentID=0, got %d", s.SegmentID)
	}
	if len(s.CompletedCells) != 0 {
		t.Errorf("expected empty CompletedCells, got %d entries", len(s.CompletedCells))
	}
}

func TestReconstructState_WithResults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "matrix.jsonl")

	// Write config + 3 results: 2 success, 1 context_limit.
	err := WriteConfig(path, MatrixConfig{
		Name:                   "test-matrix",
		RepoIDs:                []string{"repoA", "repoB"},
		TaskIDs:                []string{"task1", "task2"},
		TotalCells:             4,
		MaxCells:               100,
		MaxConsecutiveFailures: 5,
		Timeout:                "300s",
	})
	if err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	results := []CellResult{
		{Repo: "repoA", Task: "task1", Outcome: OutcomeSuccess, DurationMs: 1000, FilesChanged: 2, TokensUsed: 500},
		{Repo: "repoA", Task: "task2", Outcome: OutcomeSuccess, DurationMs: 2000, FilesChanged: 1, TokensUsed: 300},
		{Repo: "repoB", Task: "task1", Outcome: OutcomeContextLimit, DurationMs: 5000, ExitCode: 1, TokensUsed: 8000, FailureReason: "ran out of context"},
	}
	for _, cr := range results {
		if err := WriteCellResult(path, cr); err != nil {
			t.Fatalf("WriteCellResult: %v", err)
		}
	}

	s, err := ReconstructState(path)
	if err != nil {
		t.Fatalf("ReconstructState: %v", err)
	}

	if s.SegmentID != 1 {
		t.Errorf("SegmentID: want 1, got %d", s.SegmentID)
	}
	if s.CellCount != 3 {
		t.Errorf("CellCount: want 3, got %d", s.CellCount)
	}
	if s.SuccessCount != 2 {
		t.Errorf("SuccessCount: want 2, got %d", s.SuccessCount)
	}
	if s.FailureCount != 1 {
		t.Errorf("FailureCount: want 1, got %d", s.FailureCount)
	}
	if s.ConsecutiveFailures != 1 {
		t.Errorf("ConsecutiveFailures: want 1, got %d", s.ConsecutiveFailures)
	}
	if s.TotalDurationMs != 8000 {
		t.Errorf("TotalDurationMs: want 8000, got %d", s.TotalDurationMs)
	}
	if s.TotalTokens != 8800 {
		t.Errorf("TotalTokens: want 8800, got %d", s.TotalTokens)
	}
	if len(s.Results) != 3 {
		t.Errorf("Results length: want 3, got %d", len(s.Results))
	}
	if !s.CompletedCells["repoA:task1"] {
		t.Error("CompletedCells missing repoA:task1")
	}
	if !s.CompletedCells["repoB:task1"] {
		t.Error("CompletedCells missing repoB:task1")
	}
	if s.Config.Name != "test-matrix" {
		t.Errorf("Config.Name: want test-matrix, got %s", s.Config.Name)
	}
}

func TestCheckCircuitBreaker(t *testing.T) {
	t.Run("consecutive_failures_exceeded", func(t *testing.T) {
		s := &MatrixState{
			Config: MatrixConfig{
				MaxConsecutiveFailures: 5,
				MaxCells:               100,
			},
			ConsecutiveFailures: 5,
			CompletedCells:      make(map[string]bool),
		}
		err := s.CheckCircuitBreaker()
		if err == nil {
			t.Fatal("expected circuit breaker error for 5 consecutive failures")
		}
	})

	t.Run("max_cells_exceeded", func(t *testing.T) {
		s := &MatrixState{
			Config: MatrixConfig{
				MaxConsecutiveFailures: 5,
				MaxCells:               10,
			},
			CellCount:      10,
			CompletedCells: make(map[string]bool),
		}
		err := s.CheckCircuitBreaker()
		if err == nil {
			t.Fatal("expected circuit breaker error for max cells")
		}
	})

	t.Run("within_limits", func(t *testing.T) {
		s := &MatrixState{
			Config: MatrixConfig{
				MaxConsecutiveFailures: 5,
				MaxCells:               100,
			},
			CellCount:           3,
			ConsecutiveFailures: 2,
			CompletedCells:      make(map[string]bool),
		}
		err := s.CheckCircuitBreaker()
		if err != nil {
			t.Fatalf("expected no error within limits, got %v", err)
		}
	})
}

func TestWriteConfig_WriteCellResult(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roundtrip.jsonl")

	// Write config.
	cfg := MatrixConfig{
		Name:                   "roundtrip-test",
		RepoIDs:                []string{"r1"},
		TaskIDs:                []string{"t1"},
		TotalCells:             1,
		MaxCells:               50,
		MaxConsecutiveFailures: 3,
		Timeout:                "60s",
	}
	if err := WriteConfig(path, cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	// Write cell result.
	cr := CellResult{
		Repo:             "r1",
		Task:             "t1",
		Outcome:          OutcomePartial,
		Severity:         SeverityDegraded,
		ValidationPassed: false,
		DurationMs:       3500,
		ExitCode:         0,
		FilesChanged:     4,
		TokensUsed:       1200,
		LLMAnalysis:      "partial implementation, missing edge case",
		PhasesReached:    []string{"clone", "execute", "validate"},
	}
	if err := WriteCellResult(path, cr); err != nil {
		t.Fatalf("WriteCellResult: %v", err)
	}

	// Verify file exists and has content.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("JSONL file is empty after writes")
	}

	// Reconstruct and verify.
	s, err := ReconstructState(path)
	if err != nil {
		t.Fatalf("ReconstructState: %v", err)
	}

	if s.Config.Name != "roundtrip-test" {
		t.Errorf("Config.Name: want roundtrip-test, got %s", s.Config.Name)
	}
	if s.CellCount != 1 {
		t.Errorf("CellCount: want 1, got %d", s.CellCount)
	}
	if s.PartialCount != 1 {
		t.Errorf("PartialCount: want 1, got %d", s.PartialCount)
	}
	if s.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures: want 0 (partial resets), got %d", s.ConsecutiveFailures)
	}
	if !s.CompletedCells["r1:t1"] {
		t.Error("CompletedCells missing r1:t1")
	}
	if len(s.Results) != 1 {
		t.Fatalf("Results length: want 1, got %d", len(s.Results))
	}
	r := s.Results[0]
	if r.Outcome != OutcomePartial {
		t.Errorf("Result.Outcome: want partial, got %s", r.Outcome)
	}
	if r.Severity != SeverityDegraded {
		t.Errorf("Result.Severity: want degraded, got %s", r.Severity)
	}
	if r.TokensUsed != 1200 {
		t.Errorf("Result.TokensUsed: want 1200, got %d", r.TokensUsed)
	}
	if len(r.PhasesReached) != 3 {
		t.Errorf("Result.PhasesReached length: want 3, got %d", len(r.PhasesReached))
	}
}
