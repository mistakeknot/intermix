package eval

import (
	"strings"
	"testing"
)

func TestBuildDebugBeadTitle(t *testing.T) {
	title := BuildDebugBeadTitle("chi", "add-test", OutcomeTimeout)
	expected := "Stress test failure: chi/add-test — timeout"
	if title != expected {
		t.Errorf("expected %q, got %q", expected, title)
	}
}

func TestBuildDebugBeadDescription(t *testing.T) {
	cr := CellResult{
		Repo:       "chi",
		Task:       "add-test",
		Outcome:    OutcomeTimeout,
		Severity:   SeverityCritical,
		DurationMs: 300000,
		ExitCode:   -1,
	}
	desc := BuildDebugBeadDescription(cr, "session exited with timeout", "last 20 lines of output here")
	if desc == "" {
		t.Error("expected non-empty description")
	}
	// Should contain key fields
	for _, substr := range []string{"chi", "add-test", "timeout", "critical", "300000"} {
		if !strings.Contains(desc, substr) {
			t.Errorf("description missing %q", substr)
		}
	}
}

func TestBuildDebugBeadDescriptionWithOptionalFields(t *testing.T) {
	cr := CellResult{
		Repo:          "zod",
		Task:          "refactor-extract",
		Outcome:       OutcomeCrash,
		Severity:      SeverityDegraded,
		DurationMs:    15000,
		ExitCode:      1,
		FailureReason: "segfault in parser",
		LLMAnalysis:   "The agent attempted an invalid memory access",
	}
	desc := BuildDebugBeadDescription(cr, "", "")
	if !strings.Contains(desc, "segfault in parser") {
		t.Error("description missing failure reason")
	}
	if !strings.Contains(desc, "invalid memory access") {
		t.Error("description missing LLM analysis")
	}
	// Empty evidence and pane capture should not appear
	if strings.Contains(desc, "Evidence Excerpt") {
		t.Error("description should not contain evidence section when empty")
	}
	if strings.Contains(desc, "Pane Capture") {
		t.Error("description should not contain pane capture section when empty")
	}
}

func TestBuildPatternBeadTitle(t *testing.T) {
	cluster := FailureCluster{
		Outcome: OutcomeTimeout,
		Count:   3,
		Cells:   []string{"chi:add-test", "zod:add-test", "click:add-test"},
	}
	title := BuildPatternBeadTitle(cluster)
	expected := "Pattern: timeout across 3 cells"
	if title != expected {
		t.Errorf("expected %q, got %q", expected, title)
	}
}

func TestParseBeadIDFromOutput(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "standard format",
			input:    "✓ Created issue: Sylveste-abc1 — Stress test failure: chi/add-test",
			expected: "Sylveste-abc1",
		},
		{
			name:     "no marker",
			input:    "Error: something went wrong",
			expected: "",
		},
		{
			name:     "trailing newline",
			input:    "✓ Created issue: Sylveste-xyz9 — some title\n",
			expected: "Sylveste-xyz9",
		},
		{
			name:     "no space after ID",
			input:    "Created issue: Sylveste-9z2f",
			expected: "Sylveste-9z2f",
		},
		{
			name:     "empty output",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseBeadIDFromOutput(tt.input)
			if got != tt.expected {
				t.Errorf("parseBeadIDFromOutput(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
