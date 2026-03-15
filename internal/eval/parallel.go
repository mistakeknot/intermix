package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// BatchCell represents a single cell in the stress test matrix.
type BatchCell struct {
	Repo Repo
	Task Task
}

// ID returns a unique identifier for this cell.
func (c BatchCell) ID() string {
	return fmt.Sprintf("%s-%s", c.Repo.ID, c.Task.ID)
}

// BatchResult holds the outcome of a single cell execution.
type BatchResult struct {
	Cell        BatchCell
	SessionName string
	RunDetails  *RunDetails
	CellResult  *CellResult
	Evidence    string // Path to harvested evidence file
	PaneCapture string // Last N lines of tmux pane output
	Error       error
}

// BuildBatchCells expands the manifest into individual cells, optionally filtered.
// If repoFilter is non-nil, only repos with IDs in the filter are included.
// Task.Repos field is respected: if a task has Repos set, it only runs on those repos.
func BuildBatchCells(manifest *Manifest, repoFilter []string) []BatchCell {
	filterSet := make(map[string]bool)
	for _, id := range repoFilter {
		filterSet[id] = true
	}

	var cells []BatchCell
	for _, repo := range manifest.Repos {
		if len(repoFilter) > 0 && !filterSet[repo.ID] {
			continue
		}
		for _, task := range manifest.Tasks {
			// If task is repo-specific, skip non-matching repos
			if len(task.Repos) > 0 {
				match := false
				for _, r := range task.Repos {
					if r == repo.ID {
						match = true
						break
					}
				}
				if !match {
					continue
				}
			}
			cells = append(cells, BatchCell{Repo: repo, Task: task})
		}
	}
	return cells
}

// RunBatch launches all cells in parallel tmux sessions.
// Each cell: clone -> setup -> spawn Skaffen in tmux.
// Returns immediately with session names for monitoring.
func RunBatch(ctx context.Context, cells []BatchCell, workDir string, defaultTimeout time.Duration) []BatchResult {
	results := make([]BatchResult, len(cells))
	var wg sync.WaitGroup

	for i, cell := range cells {
		wg.Add(1)
		go func(idx int, c BatchCell) {
			defer wg.Done()

			cellID := c.ID()
			cloneDir := filepath.Join(os.TempDir(), "intermix", cellID)

			// Ensure cells directory exists
			cellsDir := filepath.Join(workDir, "cells")
			os.MkdirAll(cellsDir, 0755)

			// Clone
			if err := CloneRepo(c.Repo.URL, cloneDir); err != nil {
				results[idx] = BatchResult{Cell: c, Error: fmt.Errorf("clone failed: %w", err)}
				return
			}

			// Setup
			if c.Repo.Setup != "" {
				if err := RunSetup(cloneDir, c.Repo.Setup); err != nil {
					results[idx] = BatchResult{Cell: c, Error: fmt.Errorf("setup failed: %w", err)}
					return
				}
			}

			// Spawn in tmux
			timeout := defaultTimeout
			if c.Repo.SkaffenConfig.Timeout != "" {
				if parsed, err := time.ParseDuration(c.Repo.SkaffenConfig.Timeout); err == nil {
					timeout = parsed
				}
			}

			sessionName, err := SpawnSkaffenTmux(ctx, c.Repo.ID, c.Task.ID, cloneDir, c.Task.Prompt, timeout.String())
			if err != nil {
				results[idx] = BatchResult{Cell: c, Error: fmt.Errorf("spawn failed: %w", err)}
				return
			}

			results[idx] = BatchResult{
				Cell:        c,
				SessionName: sessionName,
			}
		}(i, cell)
	}

	wg.Wait()
	return results
}

// PollBatch waits for active sessions to complete, collecting results.
// For each completed cell: builds RunDetails, runs validation, classifies
// via ClassifyFromRunDetails, writes per-cell JSONL via WriteCellResult +
// CellJSONLPath, writes run details to CellRunFilePath.
//
// Respects ctx cancellation — if the context is cancelled (e.g., MCP timeout),
// goroutines that haven't finished are abandoned. Per-cell files are written
// as each cell completes, so partial results survive cancellation.
func PollBatch(ctx context.Context, results []BatchResult, workDir string, timeout time.Duration) {
	done := make(chan struct{})

	for i := range results {
		if results[i].Error != nil || results[i].SessionName == "" {
			continue
		}
		go func(idx int) {

			r := &results[idx]
			stdout, exitCode, durationMs, err := WaitTmuxSession(ctx, r.SessionName, timeout)

			r.PaneCapture = stdout

			cloneDir := filepath.Join(os.TempDir(), "intermix", r.Cell.ID())

			// Build RunDetails
			rd := &RunDetails{
				CellID:       r.Cell.ID(),
				Repo:         r.Cell.Repo.ID,
				Task:         r.Cell.Task.ID,
				Stdout:       truncateOutput(stdout, 65536),
				Stderr:       "", // tmux captures combined output
				DurationMs:   durationMs,
				ExitCode:     exitCode,
				CloneDir:     cloneDir,
				FilesChanged: countFilesChanged(cloneDir),
			}

			if err != nil {
				rd.ExitCode = -1
				rd.Stderr = err.Error()
			}

			// Extract token usage from Skaffen's output
			ParseSkaffenTokens(rd, stdout)

			// Run validation if available
			validationCmd := r.Cell.Task.ValidationCmd
			if validationCmd == "" {
				validationCmd = InferValidationCmd(cloneDir, r.Cell.Repo.Language)
			}
			if validationCmd != "" {
				vr := RunValidation(cloneDir, validationCmd)
				rd.ValidationPassed = vr.Passed
				rd.ValidationOutput = vr.Output
			}

			r.RunDetails = rd

			// Classify
			cr := ClassifyFromRunDetails(rd, r.Cell.Repo.ID, r.Cell.Task.ID)
			r.CellResult = &cr

			// Write per-cell JSONL
			cellJSONL := CellJSONLPath(workDir, r.Cell.ID())
			os.MkdirAll(filepath.Dir(cellJSONL), 0755)
			WriteCellResult(cellJSONL, cr)

			// Write run details for debugging
			runFile := CellRunFilePath(workDir, r.Cell.ID())
			writeRunDetailsToPath(runFile, rd)

			// Signal completion (non-blocking)
			select {
			case done <- struct{}{}:
			default:
			}
		}(i)
	}

	// Count how many cells we're waiting for
	pending := 0
	for _, r := range results {
		if r.Error == nil && r.SessionName != "" {
			pending++
		}
	}

	// Wait for all cells or context cancellation
	completed := 0
	for completed < pending {
		select {
		case <-done:
			completed++
		case <-ctx.Done():
			return // Context cancelled — partial results already written to disk
		}
	}
}

// writeRunDetailsToPath writes RunDetails to a specific JSON file path
// for post-mortem analysis. Unlike the tools.go writeRunDetails which
// always writes to .intermix-run.json in a directory, this writes to an
// explicit path for parallel cell isolation.
func writeRunDetailsToPath(path string, rd *RunDetails) {
	os.MkdirAll(filepath.Dir(path), 0755)
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	enc.Encode(rd)
}
