package eval

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RunDetails captures the full result of executing a cell (clone + skaffen + validation).
type RunDetails struct {
	CellID           string `json:"cell_id"`
	Repo             string `json:"repo"`
	Task             string `json:"task"`
	ExitCode         int    `json:"exit_code"`
	DurationMs       int64  `json:"duration_ms"`
	Stdout           string `json:"stdout"`
	Stderr           string `json:"stderr"`
	FilesChanged     int    `json:"files_changed"`
	ValidationPassed bool   `json:"validation_passed"`
	ValidationOutput string `json:"validation_output"`
	CloneDir         string `json:"clone_dir"`
}

// ValidationResult captures the outcome of running a validation command.
type ValidationResult struct {
	Passed   bool
	ExitCode int
	Output   string
}

// CloneRepo performs a shallow git clone of url into destDir.
func CloneRepo(url, destDir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "clone", "--depth=1", url, destDir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone %s: %w: %s", url, err, stderr.String())
	}
	return nil
}

// RunSetup executes a setup command in the given directory with a 120s timeout.
func RunSetup(dir, setupCmd string) error {
	if setupCmd == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", setupCmd)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("setup command %q: %w: %s", setupCmd, err, stderr.String())
	}
	return nil
}

// SpawnSkaffen runs skaffen in print mode against the given directory.
// It returns a RunDetails populated with stdout, stderr, exit code, duration,
// and file-change count. The timeout parameter should be a duration string
// (e.g. "300s"); it defaults to 300s if empty.
func SpawnSkaffen(dir, prompt, timeout string) *RunDetails {
	dur := 300 * time.Second
	if timeout != "" {
		if parsed, err := time.ParseDuration(timeout); err == nil {
			dur = parsed
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), dur)
	defer cancel()

	cmd := exec.CommandContext(ctx, "skaffen", "--mode", "print", "--prompt", prompt)
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)

	rd := &RunDetails{
		DurationMs: elapsed.Milliseconds(),
		Stdout:     truncateOutput(stdout.String(), 64*1024),
		Stderr:     truncateOutput(stderr.String(), 16*1024),
		CloneDir:   dir,
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			rd.ExitCode = exitErr.ExitCode()
		} else {
			rd.ExitCode = -1
		}
	}

	rd.FilesChanged = countFilesChanged(dir)
	return rd
}

// RunValidation executes a validation command in dir with a 120s timeout.
func RunValidation(dir, validationCmd string) ValidationResult {
	if validationCmd == "" {
		return ValidationResult{Passed: true, Output: "no validation command configured"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", validationCmd)
	cmd.Dir = dir

	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined

	err := cmd.Run()
	output := truncateOutput(combined.String(), 32*1024)

	if err != nil {
		exitCode := -1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		return ValidationResult{
			Passed:   false,
			ExitCode: exitCode,
			Output:   output,
		}
	}

	return ValidationResult{
		Passed:   true,
		ExitCode: 0,
		Output:   output,
	}
}

// InferValidationCmd returns a sensible default test command based on the
// language string. Returns empty string if the language is not recognized.
func InferValidationCmd(dir, language string) string {
	switch strings.ToLower(language) {
	case "go", "golang":
		return "go test ./..."
	case "ts", "typescript", "js", "javascript":
		// Check for package.json test script presence.
		if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
			return "npm test"
		}
		return "npm test"
	case "python", "py":
		return "pytest"
	case "rust", "rs":
		return "cargo test"
	case "java":
		if _, err := os.Stat(filepath.Join(dir, "gradlew")); err == nil {
			return "./gradlew test"
		}
		return "mvn test"
	default:
		return ""
	}
}

// countFilesChanged uses git status --porcelain to count changed files.
func countFilesChanged(dir string) int {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range bytes.Split(out, []byte("\n")) {
		if len(bytes.TrimSpace(line)) > 0 {
			count++
		}
	}
	return count
}

// truncateOutput trims s to at most maxBytes, appending a truncation notice
// if anything was cut. It avoids splitting in the middle of a UTF-8 sequence
// by backing up to the last valid rune boundary.
func truncateOutput(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Back up to avoid splitting a multi-byte rune.
	truncated := s[:maxBytes]
	for i := len(truncated) - 1; i >= len(truncated)-4 && i >= 0; i-- {
		if truncated[i] < 0x80 || truncated[i] >= 0xC0 {
			truncated = truncated[:i+1]
			break
		}
	}
	return truncated + "\n... [truncated]"
}
