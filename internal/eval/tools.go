package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterAll registers all intermix tools with the MCP server.
func RegisterAll(s *server.MCPServer) {
	s.AddTool(initMatrixTool, handleInitMatrix)
	s.AddTool(runCellTool, handleRunCell)
	s.AddTool(classifyResultTool, handleClassifyResult)
	s.AddTool(reportMatrixTool, handleReportMatrix)
}

var initMatrixTool = mcp.NewTool("init_matrix",
	mcp.WithDescription("Initialize a matrix evaluation campaign from a YAML manifest. Validates repos/tasks, expands the matrix, writes config to JSONL."),
	mcp.WithString("manifest_path", mcp.Required(), mcp.Description("Path to intermix.yaml manifest file")),
	mcp.WithString("name", mcp.Required(), mcp.Description("Campaign name (e.g., 'skaffen-v1-stress')")),
	mcp.WithString("working_directory", mcp.Description("Directory for intermix.jsonl (default: cwd)")),
	mcp.WithString("bead_id", mcp.Description("Parent bead ID for linking failure beads")),
)

var runCellTool = mcp.NewTool("run_cell",
	mcp.WithDescription("Execute a single (repo, task) cell: clone repo, run setup, spawn Skaffen, run validation. Returns structured result."),
	mcp.WithString("repo", mcp.Required(), mcp.Description("Repo ID from the manifest")),
	mcp.WithString("task", mcp.Required(), mcp.Description("Task ID from the manifest")),
	mcp.WithString("working_directory", mcp.Description("Directory containing intermix.jsonl (default: cwd)")),
)

var classifyResultTool = mcp.NewTool("classify_result",
	mcp.WithDescription("Apply hybrid taxonomy to the last run_cell result. Reads .intermix-run.json, classifies outcome, optionally adds LLM analysis, writes to JSONL."),
	mcp.WithString("llm_analysis", mcp.Description("Optional LLM-generated analysis of the failure (free text)")),
	mcp.WithString("working_directory", mcp.Description("Directory containing intermix.jsonl (default: cwd)")),
)

var reportMatrixTool = mcp.NewTool("report_matrix",
	mcp.WithDescription("Generate a campaign report: pass/fail heatmap, failure clusters, delta comparison vs. previous campaign. Optionally creates beads for failure patterns."),
	mcp.WithString("working_directory", mcp.Description("Directory containing intermix.jsonl (default: cwd)")),
	mcp.WithString("bead_id", mcp.Description("Parent bead ID — failure clusters with ≥2 cells auto-create child beads")),
	mcp.WithString("format", mcp.Description("Output format: 'text' (default) or 'json'")),
)

func resolveDir(req mcp.CallToolRequest) string {
	dir := req.GetString("working_directory", "")
	if dir == "" {
		dir, _ = os.Getwd()
	}
	return dir
}

func handleInitMatrix(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	manifestPath := req.GetString("manifest_path", "")
	name := req.GetString("name", "")
	dir := resolveDir(req)
	beadID := req.GetString("bead_id", "")

	if manifestPath == "" || name == "" {
		return mcp.NewToolResultText("missing required fields: manifest_path, name"), nil
	}

	m, err := ParseManifest(manifestPath)
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("manifest error: %v", err)), nil
	}

	cells := ExpandMatrix(m)

	repoIDs := make([]string, len(m.Repos))
	for i, r := range m.Repos {
		repoIDs[i] = r.ID
	}
	taskIDs := make([]string, len(m.Tasks))
	for i, t := range m.Tasks {
		taskIDs[i] = t.ID
	}

	cfg := MatrixConfig{
		Name:                   name,
		ManifestPath:           manifestPath,
		RepoIDs:                repoIDs,
		TaskIDs:                taskIDs,
		TotalCells:             len(cells),
		MaxCells:               m.Defaults.MaxCells,
		MaxConsecutiveFailures: m.Defaults.MaxConsecutiveFailures,
		Timeout:                m.Defaults.Timeout,
		BeadID:                 beadID,
	}

	jsonlPath := filepath.Join(dir, "intermix.jsonl")
	if err := WriteConfig(jsonlPath, cfg); err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("write config: %v", err)), nil
	}

	// Write manifest cache for run_cell to read
	manifestCache, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(dir, ".intermix-manifest.json"), manifestCache, 0644)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Campaign '%s' initialized.\n", name))
	sb.WriteString(fmt.Sprintf("Repos: %d | Tasks: %d | Total cells: %d\n", len(m.Repos), len(m.Tasks), len(cells)))
	sb.WriteString(fmt.Sprintf("Limits: max_cells=%d, max_consecutive_failures=%d, timeout=%s\n",
		cfg.MaxCells, cfg.MaxConsecutiveFailures, cfg.Timeout))
	sb.WriteString("\nCells to evaluate:\n")
	for _, c := range cells {
		sb.WriteString(fmt.Sprintf("  - %s × %s\n", c.RepoID, c.TaskID))
	}

	return mcp.NewToolResultText(sb.String()), nil
}

func handleRunCell(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	repoID := req.GetString("repo", "")
	taskID := req.GetString("task", "")
	dir := resolveDir(req)

	if repoID == "" || taskID == "" {
		return mcp.NewToolResultText("missing required fields: repo, task"), nil
	}

	// Read state and check circuit breaker
	jsonlPath := filepath.Join(dir, "intermix.jsonl")
	state, err := ReconstructState(jsonlPath)
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("state error: %v", err)), nil
	}
	if state.SegmentID == 0 {
		return mcp.NewToolResultText("no campaign initialized — run init_matrix first"), nil
	}
	if err := state.CheckCircuitBreaker(); err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("STOPPED: %v", err)), nil
	}

	// Check if already completed
	if state.CompletedCells[repoID+":"+taskID] {
		return mcp.NewToolResultText(fmt.Sprintf("cell %s:%s already completed in this campaign — skipping", repoID, taskID)), nil
	}

	// Load manifest from cache
	manifestData, err := os.ReadFile(filepath.Join(dir, ".intermix-manifest.json"))
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("manifest cache not found: %v — re-run init_matrix", err)), nil
	}
	var manifest Manifest
	json.Unmarshal(manifestData, &manifest)

	// Find repo and task
	var repo *Repo
	for i := range manifest.Repos {
		if manifest.Repos[i].ID == repoID {
			repo = &manifest.Repos[i]
			break
		}
	}
	var task *Task
	for i := range manifest.Tasks {
		if manifest.Tasks[i].ID == taskID {
			task = &manifest.Tasks[i]
			break
		}
	}
	if repo == nil || task == nil {
		return mcp.NewToolResultText(fmt.Sprintf("repo %q or task %q not found in manifest", repoID, taskID)), nil
	}

	cellID := fmt.Sprintf("%s-%s-%d", repoID, taskID, time.Now().Unix())
	cloneDir := filepath.Join(os.TempDir(), "intermix", cellID)
	os.MkdirAll(filepath.Dir(cloneDir), 0755)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Running cell: %s × %s\n", repoID, taskID))

	// 1. Clone
	sb.WriteString(fmt.Sprintf("Cloning %s...\n", repo.URL))
	if err := CloneRepo(repo.URL, cloneDir); err != nil {
		rd := &RunDetails{CellID: cellID, Repo: repoID, Task: taskID, ExitCode: -1, Stderr: err.Error()}
		writeRunDetails(dir, rd)
		return mcp.NewToolResultText(fmt.Sprintf("Clone failed: %v\n\nClassify with: classify_result (outcome will be setup_failure)", err)), nil
	}

	// 2. Setup
	if repo.Setup != "" {
		sb.WriteString(fmt.Sprintf("Running setup: %s\n", repo.Setup))
		if err := RunSetup(cloneDir, repo.Setup); err != nil {
			rd := &RunDetails{CellID: cellID, Repo: repoID, Task: taskID, ExitCode: -1, Stderr: err.Error(), CloneDir: cloneDir}
			writeRunDetails(dir, rd)
			return mcp.NewToolResultText(fmt.Sprintf("Setup failed: %v\n\nClassify with: classify_result (outcome will be setup_failure)", err)), nil
		}
	}

	// 3. Spawn Skaffen
	timeout := state.Config.Timeout
	if repo.SkaffenConfig.Timeout != "" {
		timeout = repo.SkaffenConfig.Timeout
	}
	sb.WriteString(fmt.Sprintf("Spawning Skaffen (timeout: %s)...\n", timeout))
	rd := SpawnSkaffen(cloneDir, task.Prompt, timeout)
	rd.CellID = cellID
	rd.Repo = repoID
	rd.Task = taskID

	// 4. Validate
	valCmd := task.ValidationCmd
	if valCmd == "" {
		valCmd = InferValidationCmd(cloneDir, repo.Language)
	}
	if valCmd != "" {
		sb.WriteString(fmt.Sprintf("Validating: %s\n", valCmd))
		vr := RunValidation(cloneDir, valCmd)
		rd.ValidationPassed = vr.Passed
		rd.ValidationOutput = vr.Output
	}

	// Save run details for classify_result
	writeRunDetails(dir, rd)

	sb.WriteString(fmt.Sprintf("\nResult: exit=%d, duration=%dms, files_changed=%d, validation=%v\n",
		rd.ExitCode, rd.DurationMs, rd.FilesChanged, rd.ValidationPassed))

	// Include tail of output
	if rd.Stdout != "" {
		lines := strings.Split(rd.Stdout, "\n")
		start := len(lines) - 20
		if start < 0 {
			start = 0
		}
		sb.WriteString(fmt.Sprintf("\nOutput (last 20 lines):\n%s\n", strings.Join(lines[start:], "\n")))
	}

	sb.WriteString("\nNext: call classify_result to record the outcome.")

	return mcp.NewToolResultText(sb.String()), nil
}

func handleClassifyResult(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	dir := resolveDir(req)
	llmAnalysis := req.GetString("llm_analysis", "")

	// Read run details
	rd, err := readRunDetails(dir)
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("no run details found: %v — run run_cell first", err)), nil
	}

	// Classify
	cr := ClassifyFromRunDetails(rd, rd.Repo, rd.Task)
	cr.LLMAnalysis = llmAnalysis

	// Handle setup failures explicitly
	if rd.ExitCode == -1 && rd.FilesChanged == 0 && rd.DurationMs == 0 {
		cr.Outcome = OutcomeSetupFailure
		cr.Severity = SeverityCritical
		cr.FailureReason = rd.Stderr
	}

	// Write to JSONL
	jsonlPath := filepath.Join(dir, "intermix.jsonl")
	if err := WriteCellResult(jsonlPath, cr); err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("write error: %v", err)), nil
	}

	// Emit ic event (best-effort)
	state, _ := ReconstructState(jsonlPath)
	EmitCellEvent(cr, state.Config.BeadID)

	// Clean up run details
	os.Remove(filepath.Join(dir, ".intermix-run.json"))

	return mcp.NewToolResultText(fmt.Sprintf("Classified: %s:%s → %s (%s)\nAnalysis: %s",
		cr.Repo, cr.Task, cr.Outcome, cr.Severity, cr.LLMAnalysis)), nil
}

func handleReportMatrix(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	dir := resolveDir(req)
	format := req.GetString("format", "text")

	jsonlPath := filepath.Join(dir, "intermix.jsonl")
	state, err := ReconstructState(jsonlPath)
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("state error: %v", err)), nil
	}
	if state.SegmentID == 0 {
		return mcp.NewToolResultText("no campaign found — run init_matrix first"), nil
	}

	// For delta: load previous segment if exists (segment > 1)
	var prevResults []CellResult
	if state.SegmentID > 1 {
		prevResults = loadPreviousSegment(jsonlPath, state.SegmentID-1)
	}

	report := GenerateReport(state.Results, prevResults)

	// Emit campaign event
	EmitCampaignEvent(report, state.Config.Name, state.Config.BeadID)

	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}

	return mcp.NewToolResultText(FormatReport(report)), nil
}

func writeRunDetails(dir string, rd *RunDetails) {
	data, _ := json.Marshal(rd)
	os.WriteFile(filepath.Join(dir, ".intermix-run.json"), data, 0644)
}

func readRunDetails(dir string) (*RunDetails, error) {
	data, err := os.ReadFile(filepath.Join(dir, ".intermix-run.json"))
	if err != nil {
		return nil, err
	}
	var rd RunDetails
	if err := json.Unmarshal(data, &rd); err != nil {
		return nil, err
	}
	return &rd, nil
}

// loadPreviousSegment finds results from a specific segment number.
func loadPreviousSegment(path string, targetSegment int) []CellResult {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	segment := 0
	var results []CellResult

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Contains(line, `"type":"config"`) {
			segment++
			if segment > targetSegment {
				break
			}
			results = nil // reset for current segment
		} else if segment == targetSegment && strings.Contains(line, `"type":"cell_result"`) {
			var cr CellResult
			json.Unmarshal([]byte(line), &cr)
			results = append(results, cr)
		}
	}
	return results
}
