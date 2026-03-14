package eval

import "testing"

func TestClassifyFromRunDetails_Success(t *testing.T) {
	rd := &RunDetails{
		CellID:           "cell-1",
		ExitCode:         0,
		DurationMs:       12000,
		FilesChanged:     3,
		ValidationPassed: true,
		Stderr:           "",
	}
	cr := ClassifyFromRunDetails(rd, "github.com/example/repo", "add-tests")
	if cr.Outcome != OutcomeSuccess {
		t.Errorf("outcome = %q, want %q", cr.Outcome, OutcomeSuccess)
	}
	if cr.Severity != SeverityAcceptable {
		t.Errorf("severity = %q, want %q", cr.Severity, SeverityAcceptable)
	}
	if cr.Repo != "github.com/example/repo" {
		t.Errorf("repo = %q, want %q", cr.Repo, "github.com/example/repo")
	}
	if cr.Task != "add-tests" {
		t.Errorf("task = %q, want %q", cr.Task, "add-tests")
	}
	if cr.FilesChanged != 3 {
		t.Errorf("files_changed = %d, want 3", cr.FilesChanged)
	}
	if !cr.ValidationPassed {
		t.Error("validation_passed = false, want true")
	}
}

func TestClassifyFromRunDetails_Timeout(t *testing.T) {
	rd := &RunDetails{
		CellID:           "cell-2",
		ExitCode:         -1,
		DurationMs:       300000,
		FilesChanged:     0,
		ValidationPassed: false,
		Stderr:           "error: context deadline exceeded",
	}
	cr := ClassifyFromRunDetails(rd, "github.com/example/repo", "fix-bug")
	if cr.Outcome != OutcomeTimeout {
		t.Errorf("outcome = %q, want %q", cr.Outcome, OutcomeTimeout)
	}
	if cr.Severity != SeverityCritical {
		t.Errorf("severity = %q, want %q", cr.Severity, SeverityCritical)
	}
	if cr.DurationMs != 300000 {
		t.Errorf("duration_ms = %d, want 300000", cr.DurationMs)
	}
	if cr.ExitCode != -1 {
		t.Errorf("exit_code = %d, want -1", cr.ExitCode)
	}
}

func TestClassifyFromRunDetails_NoProgress(t *testing.T) {
	rd := &RunDetails{
		CellID:           "cell-3",
		ExitCode:         0,
		DurationMs:       45000,
		FilesChanged:     0,
		ValidationPassed: false,
		Stderr:           "",
	}
	cr := ClassifyFromRunDetails(rd, "github.com/example/repo", "refactor")
	if cr.Outcome != OutcomeNoProgress {
		t.Errorf("outcome = %q, want %q", cr.Outcome, OutcomeNoProgress)
	}
	if cr.Severity != SeverityCritical {
		t.Errorf("severity = %q, want %q", cr.Severity, SeverityCritical)
	}
}

func TestClassifyFromRunDetails_Partial(t *testing.T) {
	rd := &RunDetails{
		CellID:           "cell-4",
		ExitCode:         0,
		DurationMs:       60000,
		FilesChanged:     2,
		ValidationPassed: false,
		Stderr:           "",
	}
	cr := ClassifyFromRunDetails(rd, "github.com/example/repo", "migrate-api")
	if cr.Outcome != OutcomePartial {
		t.Errorf("outcome = %q, want %q", cr.Outcome, OutcomePartial)
	}
	if cr.Severity != SeverityDegraded {
		t.Errorf("severity = %q, want %q", cr.Severity, SeverityDegraded)
	}
	if cr.FilesChanged != 2 {
		t.Errorf("files_changed = %d, want 2", cr.FilesChanged)
	}
}

func TestClassifyFromRunDetails_Crash(t *testing.T) {
	rd := &RunDetails{
		CellID:           "cell-5",
		ExitCode:         2,
		DurationMs:       5000,
		FilesChanged:     0,
		ValidationPassed: false,
		Stderr:           "goroutine 1 [running]:\npanic: runtime error: index out of range",
	}
	cr := ClassifyFromRunDetails(rd, "github.com/example/repo", "add-feature")
	if cr.Outcome != OutcomeCrash {
		t.Errorf("outcome = %q, want %q", cr.Outcome, OutcomeCrash)
	}
	if cr.Severity != SeverityCritical {
		t.Errorf("severity = %q, want %q", cr.Severity, SeverityCritical)
	}
}

func TestClassifyFromRunDetails_ContextLimit(t *testing.T) {
	rd := &RunDetails{
		CellID:           "cell-6",
		ExitCode:         0,
		DurationMs:       120000,
		FilesChanged:     1,
		ValidationPassed: false,
		Stderr:           "warning: context window exceeded, truncating",
	}
	cr := ClassifyFromRunDetails(rd, "github.com/example/repo", "large-refactor")
	if cr.Outcome != OutcomeContextLimit {
		t.Errorf("outcome = %q, want %q", cr.Outcome, OutcomeContextLimit)
	}
	if cr.Severity != SeverityCritical {
		t.Errorf("severity = %q, want %q", cr.Severity, SeverityCritical)
	}
}

func TestClassifyFromRunDetails_ToolFailure(t *testing.T) {
	rd := &RunDetails{
		CellID:           "cell-7",
		ExitCode:         1,
		DurationMs:       8000,
		FilesChanged:     0,
		ValidationPassed: false,
		Stderr:           "failed to call MCP tool: connection refused",
	}
	cr := ClassifyFromRunDetails(rd, "github.com/example/repo", "run-lint")
	if cr.Outcome != OutcomeToolFailure {
		t.Errorf("outcome = %q, want %q", cr.Outcome, OutcomeToolFailure)
	}
	if cr.Severity != SeverityDegraded {
		t.Errorf("severity = %q, want %q", cr.Severity, SeverityDegraded)
	}
}

func TestContainsAny_CaseInsensitive(t *testing.T) {
	if !containsAny("Context Deadline Exceeded", "deadline exceeded") {
		t.Error("expected case-insensitive match for 'deadline exceeded'")
	}
	if containsAny("everything is fine", "panic", "segfault") {
		t.Error("expected no match")
	}
}
