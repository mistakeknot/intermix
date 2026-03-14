package eval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Outcome constants for cell classification.
const (
	OutcomeSuccess       = "success"
	OutcomePartial       = "partial"
	OutcomeWrongApproach = "wrong_approach"
	OutcomeContextLimit  = "context_limit"
	OutcomeToolFailure   = "tool_failure"
	OutcomeNoProgress    = "no_progress"
	OutcomeCrash         = "crash"
	OutcomeTimeout       = "timeout"
	OutcomeSetupFailure  = "setup_failure"
	OutcomeSkipped       = "skipped"
)

// Severity constants for failure triage.
const (
	SeverityCritical   = "critical"
	SeverityDegraded   = "degraded"
	SeverityAcceptable = "acceptable"
)

// MatrixConfig is the config record written as the first line of each segment in the JSONL.
type MatrixConfig struct {
	Type                   string   `json:"type"` // always "config"
	Name                   string   `json:"name"`
	ManifestPath           string   `json:"manifest_path,omitempty"`
	RepoIDs                []string `json:"repo_ids"`
	TaskIDs                []string `json:"task_ids"`
	TotalCells             int      `json:"total_cells"`
	MaxCells               int      `json:"max_cells"`
	MaxConsecutiveFailures int      `json:"max_consecutive_failures"`
	Timeout                string   `json:"timeout"`
	BeadID                 string   `json:"bead_id,omitempty"`
	Timestamp              string   `json:"timestamp"`
}

// CellResult is a result record written after each cell execution.
type CellResult struct {
	Type             string   `json:"type"` // always "cell_result"
	Repo             string   `json:"repo"`
	Task             string   `json:"task"`
	Outcome          string   `json:"outcome"`
	Severity         string   `json:"severity,omitempty"`
	ValidationPassed bool     `json:"validation_passed"`
	DurationMs       int64    `json:"duration_ms"`
	ExitCode         int      `json:"exit_code"`
	FilesChanged     int      `json:"files_changed"`
	TokensUsed       int      `json:"tokens_used,omitempty"`
	LLMAnalysis      string   `json:"llm_analysis,omitempty"`
	FailureReason    string   `json:"failure_reason,omitempty"`
	PhasesReached    []string `json:"phases_reached,omitempty"`
	Timestamp        string   `json:"timestamp"`
}

// MatrixState holds the reconstructed campaign state.
type MatrixState struct {
	Config              MatrixConfig
	SegmentID           int
	CellCount           int
	SuccessCount        int
	PartialCount        int
	FailureCount        int
	SkippedCount        int
	ConsecutiveFailures int
	TotalDurationMs     int64
	TotalTokens         int
	Results             []CellResult
	CompletedCells      map[string]bool // "repo:task" -> true
}

func configDefaults(cfg *MatrixConfig) {
	if cfg.MaxCells <= 0 {
		cfg.MaxCells = 100
	}
	if cfg.MaxConsecutiveFailures <= 0 {
		cfg.MaxConsecutiveFailures = 5
	}
}

// ReconstructState reads the JSONL and rebuilds matrix state.
func ReconstructState(path string) (*MatrixState, error) {
	s := &MatrixState{
		CompletedCells: make(map[string]bool),
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read jsonl: %w", err)
	}

	// Scan newlines in-place — avoids [][]byte allocation from bytes.Split.
	for len(data) > 0 {
		var line []byte
		if idx := bytes.IndexByte(data, '\n'); idx >= 0 {
			line = data[:idx]
			data = data[idx+1:]
		} else {
			line = data
			data = nil
		}
		if len(line) == 0 {
			continue
		}

		// Byte-level type detection — avoids JSON parse for type discrimination.
		// We control the JSONL output so "type" is always present near the start.
		isConfig := bytes.Contains(line, []byte(`"type":"config"`))
		isResult := bytes.Contains(line, []byte(`"type":"cell_result"`))
		if !isConfig && !isResult {
			continue
		}

		if isConfig {
			var cfg MatrixConfig
			if err := json.Unmarshal(line, &cfg); err != nil {
				continue
			}
			configDefaults(&cfg)
			s.Config = cfg
			s.SegmentID++
			// Reset counters for new segment.
			s.CellCount = 0
			s.SuccessCount = 0
			s.PartialCount = 0
			s.FailureCount = 0
			s.SkippedCount = 0
			s.ConsecutiveFailures = 0
			s.TotalDurationMs = 0
			s.TotalTokens = 0
			s.Results = nil
			s.CompletedCells = make(map[string]bool)
		} else {
			var cr CellResult
			if err := json.Unmarshal(line, &cr); err != nil {
				continue
			}
			s.CellCount++
			s.TotalDurationMs += cr.DurationMs
			s.TotalTokens += cr.TokensUsed
			s.Results = append(s.Results, cr)
			s.CompletedCells[cr.Repo+":"+cr.Task] = true

			switch cr.Outcome {
			case OutcomeSuccess:
				s.SuccessCount++
				s.ConsecutiveFailures = 0
			case OutcomePartial:
				s.PartialCount++
				s.ConsecutiveFailures = 0
			case OutcomeSkipped:
				s.SkippedCount++
				// Skipped does not affect consecutive failures.
			default:
				// All other outcomes are failures.
				s.FailureCount++
				s.ConsecutiveFailures++
			}
		}
	}

	return s, nil
}

// CheckCircuitBreaker returns an error if any limit is exceeded.
func (s *MatrixState) CheckCircuitBreaker() error {
	cfg := s.Config
	configDefaults(&cfg)
	if s.ConsecutiveFailures >= cfg.MaxConsecutiveFailures {
		return fmt.Errorf("circuit breaker: %d consecutive failures (limit %d)", s.ConsecutiveFailures, cfg.MaxConsecutiveFailures)
	}
	if s.CellCount >= cfg.MaxCells {
		return fmt.Errorf("circuit breaker: %d cells executed (limit %d)", s.CellCount, cfg.MaxCells)
	}
	return nil
}

// WriteConfig appends a config entry to the JSONL.
func WriteConfig(path string, cfg MatrixConfig) error {
	cfg.Type = "config"
	cfg.Timestamp = time.Now().UTC().Format(time.RFC3339)
	configDefaults(&cfg)
	return appendJSONL(path, cfg)
}

// WriteCellResult appends a cell result entry to the JSONL.
func WriteCellResult(path string, cr CellResult) error {
	cr.Type = "cell_result"
	cr.Timestamp = time.Now().UTC().Format(time.RFC3339)
	return appendJSONL(path, cr)
}

// CellJSONLPath returns the JSONL path for a specific cell in parallel mode.
func CellJSONLPath(workDir, cellID string) string {
	return filepath.Join(workDir, "cells", cellID+".jsonl")
}

// CellRunFilePath returns the run-details path for a specific cell.
func CellRunFilePath(workDir, cellID string) string {
	return filepath.Join(workDir, "cells", cellID+".run.json")
}

// ReconstructStateFromCellsDir reads the main config from intermix.jsonl
// and all cell results from cells/*.jsonl files.
func ReconstructStateFromCellsDir(workDir string) (*MatrixState, error) {
	// Read config from main file.
	mainPath := filepath.Join(workDir, "intermix.jsonl")
	state, err := ReconstructState(mainPath)
	if err != nil {
		return nil, fmt.Errorf("reading main config: %w", err)
	}

	// Walk cells directory for results.
	cellsDir := filepath.Join(workDir, "cells")
	entries, err := os.ReadDir(cellsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil // No cells yet.
		}
		return nil, fmt.Errorf("reading cells dir: %w", err)
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		cellPath := filepath.Join(cellsDir, entry.Name())
		data, err := os.ReadFile(cellPath)
		if err != nil {
			continue
		}
		for len(data) > 0 {
			var line []byte
			if idx := bytes.IndexByte(data, '\n'); idx >= 0 {
				line = data[:idx]
				data = data[idx+1:]
			} else {
				line = data
				data = nil
			}
			if len(line) == 0 {
				continue
			}
			if !bytes.Contains(line, []byte(`"type":"cell_result"`)) {
				continue
			}
			var cr CellResult
			if err := json.Unmarshal(line, &cr); err != nil {
				continue
			}
			state.CellCount++
			state.TotalDurationMs += cr.DurationMs
			state.TotalTokens += cr.TokensUsed
			state.CompletedCells[cr.Repo+":"+cr.Task] = true
			state.Results = append(state.Results, cr)

			switch cr.Outcome {
			case OutcomeSuccess:
				state.SuccessCount++
				state.ConsecutiveFailures = 0
			case OutcomePartial:
				state.PartialCount++
				state.ConsecutiveFailures = 0
			case OutcomeSkipped:
				state.SkippedCount++
			default:
				state.FailureCount++
				state.ConsecutiveFailures++
			}
		}
	}
	return state, nil
}

func appendJSONL(path string, v interface{}) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
