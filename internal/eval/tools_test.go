package eval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func TestHandleInitMatrix(t *testing.T) {
	dir := t.TempDir()

	// Write a valid manifest
	yaml := `
repos:
  - id: test-repo
    url: /dev/null
    language: go
tasks:
  - id: test-task
    prompt: "Test prompt"
`
	manifestPath := filepath.Join(dir, "intermix.yaml")
	os.WriteFile(manifestPath, []byte(yaml), 0644)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"manifest_path":     manifestPath,
		"working_directory": dir,
		"name":              "test-campaign",
	}

	result, err := handleInitMatrix(context.Background(), req)
	if err != nil {
		t.Fatalf("handleInitMatrix: %v", err)
	}

	// Check JSONL was created
	jsonlPath := filepath.Join(dir, "intermix.jsonl")
	if _, err := os.Stat(jsonlPath); err != nil {
		t.Error("intermix.jsonl not created")
	}

	// Check manifest cache was created
	if _, err := os.Stat(filepath.Join(dir, ".intermix-manifest.json")); err != nil {
		t.Error(".intermix-manifest.json not created")
	}

	// Check result text contains cell count
	text := result.Content[0].(mcp.TextContent).Text
	if text == "" {
		t.Error("empty result text")
	}
}

func TestHandleClassifyResult(t *testing.T) {
	dir := t.TempDir()

	// Set up: write a config to JSONL first
	cfg := MatrixConfig{Name: "test", MaxCells: 100, MaxConsecutiveFailures: 5, Timeout: "300s"}
	jsonlPath := filepath.Join(dir, "intermix.jsonl")
	WriteConfig(jsonlPath, cfg)

	// Write run details
	rd := &RunDetails{
		CellID:           "test-1",
		Repo:             "chi",
		Task:             "add-test",
		ExitCode:         0,
		DurationMs:       5000,
		FilesChanged:     2,
		ValidationPassed: true,
	}
	writeRunDetails(dir, rd)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"working_directory": dir,
		"llm_analysis":      "Skaffen found an untested helper and wrote a passing test.",
	}

	result, err := handleClassifyResult(context.Background(), req)
	if err != nil {
		t.Fatalf("handleClassifyResult: %v", err)
	}

	text := result.Content[0].(mcp.TextContent).Text
	if text == "" {
		t.Error("empty result text")
	}

	// Verify result was written to JSONL
	state, _ := ReconstructState(jsonlPath)
	if state.CellCount != 1 {
		t.Errorf("cell count: got %d, want 1", state.CellCount)
	}
	if state.SuccessCount != 1 {
		t.Errorf("success count: got %d, want 1", state.SuccessCount)
	}

	// Verify run details cleaned up
	if _, err := os.Stat(filepath.Join(dir, ".intermix-run.json")); err == nil {
		t.Error(".intermix-run.json should be deleted after classify")
	}
}

func TestHandleReportMatrix(t *testing.T) {
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "intermix.jsonl")

	// Write a campaign
	cfg := MatrixConfig{Name: "test", MaxCells: 100}
	WriteConfig(jsonlPath, cfg)
	WriteCellResult(jsonlPath, CellResult{Repo: "chi", Task: "add-test", Outcome: OutcomeSuccess})
	WriteCellResult(jsonlPath, CellResult{Repo: "chi", Task: "refactor", Outcome: OutcomeContextLimit, Severity: SeverityCritical})

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"working_directory": dir,
	}

	result, err := handleReportMatrix(context.Background(), req)
	if err != nil {
		t.Fatalf("handleReportMatrix: %v", err)
	}

	text := result.Content[0].(mcp.TextContent).Text
	if text == "" {
		t.Error("empty report")
	}
}

func TestFullPipeline(t *testing.T) {
	dir := t.TempDir()

	// Write a minimal manifest
	yaml := `
repos:
  - id: test-repo
    url: /dev/null
    language: go
tasks:
  - id: test-task
    prompt: "Write a test"
defaults:
  max_cells: 10
  max_consecutive_failures: 3
`
	os.WriteFile(filepath.Join(dir, "intermix.yaml"), []byte(yaml), 0644)

	// Step 1: init_matrix
	initReq := mcp.CallToolRequest{}
	initReq.Params.Arguments = map[string]interface{}{
		"manifest_path":     filepath.Join(dir, "intermix.yaml"),
		"name":              "integration-test",
		"working_directory": dir,
	}
	result, err := handleInitMatrix(context.Background(), initReq)
	if err != nil {
		t.Fatalf("init_matrix: %v", err)
	}
	t.Logf("init_matrix: %s", result.Content[0].(mcp.TextContent).Text)

	// Verify JSONL exists
	jsonlPath := filepath.Join(dir, "intermix.jsonl")
	if _, err := os.Stat(jsonlPath); err != nil {
		t.Fatal("intermix.jsonl not created")
	}

	// Step 2: Simulate a run by writing RunDetails directly
	rd := &RunDetails{
		CellID:           "test-repo-test-task-1",
		Repo:             "test-repo",
		Task:             "test-task",
		ExitCode:         0,
		DurationMs:       5000,
		FilesChanged:     2,
		ValidationPassed: true,
	}
	writeRunDetails(dir, rd)

	// Step 3: classify_result
	classifyReq := mcp.CallToolRequest{}
	classifyReq.Params.Arguments = map[string]interface{}{
		"working_directory": dir,
		"llm_analysis":      "Skaffen successfully identified an untested function and wrote a passing test.",
	}
	result, err = handleClassifyResult(context.Background(), classifyReq)
	if err != nil {
		t.Fatalf("classify_result: %v", err)
	}
	t.Logf("classify_result: %s", result.Content[0].(mcp.TextContent).Text)

	// Step 4: report_matrix
	reportReq := mcp.CallToolRequest{}
	reportReq.Params.Arguments = map[string]interface{}{
		"working_directory": dir,
	}
	result, err = handleReportMatrix(context.Background(), reportReq)
	if err != nil {
		t.Fatalf("report_matrix: %v", err)
	}
	t.Logf("report_matrix: %s", result.Content[0].(mcp.TextContent).Text)

	// Verify state
	state, _ := ReconstructState(jsonlPath)
	if state.SuccessCount != 1 {
		t.Errorf("success count: got %d, want 1", state.SuccessCount)
	}
}

func TestToolsRegistered(t *testing.T) {
	s := server.NewMCPServer("intermix-test", "0.0.1")
	RegisterAll(s)

	// Send a tools/list JSON-RPC request
	listReq := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	resp := s.HandleMessage(context.Background(), json.RawMessage(listReq))

	// Marshal response to inspect
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	respStr := string(data)

	expected := []string{
		"init_matrix",
		"run_cell",
		"classify_result",
		"report_matrix",
		"run_batch",
		"poll_batch",
	}
	for _, name := range expected {
		if !strings.Contains(respStr, `"`+name+`"`) {
			t.Errorf("tool %q not found in tools/list response", name)
		}
	}
}

func TestHandleRunBatch_MissingArgs(t *testing.T) {
	dir := t.TempDir()

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"working_directory": dir,
	}

	result, err := handleRunBatch(context.Background(), req)
	if err != nil {
		t.Fatalf("handleRunBatch error: %v", err)
	}

	text := result.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "missing required fields") {
		t.Errorf("expected missing-fields error, got: %s", text)
	}
}

func TestHandleRunBatch_NoManifest(t *testing.T) {
	dir := t.TempDir()

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"working_directory": dir,
		"repos":             []interface{}{"repo-a"},
		"tasks":             []interface{}{"task-a"},
	}

	result, err := handleRunBatch(context.Background(), req)
	if err != nil {
		t.Fatalf("handleRunBatch error: %v", err)
	}

	text := result.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "manifest cache not found") {
		t.Errorf("expected manifest error, got: %s", text)
	}
}

func TestHandlePollBatch_NoBatchState(t *testing.T) {
	dir := t.TempDir()

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"working_directory": dir,
	}

	result, err := handlePollBatch(context.Background(), req)
	if err != nil {
		t.Fatalf("handlePollBatch error: %v", err)
	}

	text := result.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "no batch state found") {
		t.Errorf("expected no-batch-state error, got: %s", text)
	}
}

func TestToStringSlice(t *testing.T) {
	tests := []struct {
		name string
		in   interface{}
		want int
	}{
		{"nil", nil, 0},
		{"empty", []interface{}{}, 0},
		{"strings", []interface{}{"a", "b", "c"}, 3},
		{"mixed", []interface{}{"a", 42, "b"}, 2},
		{"wrong type", "not-array", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toStringSlice(tt.in)
			if len(got) != tt.want {
				t.Errorf("toStringSlice(%v): got %d items, want %d", tt.in, len(got), tt.want)
			}
		})
	}
}
