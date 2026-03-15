package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RunDetails captures the full result of executing a cell (clone + skaffen + validation).
type RunDetails struct {
	CellID               string `json:"cell_id"`
	Repo                 string `json:"repo"`
	Task                 string `json:"task"`
	ExitCode             int    `json:"exit_code"`
	DurationMs           int64  `json:"duration_ms"`
	Stdout               string `json:"stdout"`
	Stderr               string `json:"stderr"`
	FilesChanged         int    `json:"files_changed"`
	ValidationPassed     bool   `json:"validation_passed"`
	ValidationOutput     string `json:"validation_output"`
	CloneDir             string `json:"clone_dir"`
	InputTokens          int    `json:"input_tokens,omitempty"`
	OutputTokens         int    `json:"output_tokens,omitempty"`
	CacheCreationTokens  int    `json:"cache_creation_tokens,omitempty"`
	CacheReadTokens      int    `json:"cache_read_tokens,omitempty"`
}

// parseSkaffenTokens extracts token usage from Skaffen's stderr/stdout output.
// Matches lines like:
//   [1 turns, 15 in / 6 out tokens]
//   [1 turns, 15 in / 6 out tokens, 3400 cache_read / 1200 cache_create]
var skaffenTokenRe = regexp.MustCompile(`\[(\d+) turns?, (\d+) in / (\d+) out tokens(?:, (\d+) cache_read / (\d+) cache_create)?\]`)

// ParseSkaffenTokens extracts token counts from output text.
// Populates the token fields on rd in-place.
func ParseSkaffenTokens(rd *RunDetails, output string) {
	m := skaffenTokenRe.FindStringSubmatch(output)
	if m == nil {
		return
	}
	rd.InputTokens, _ = strconv.Atoi(m[2])
	rd.OutputTokens, _ = strconv.Atoi(m[3])
	if m[4] != "" {
		rd.CacheReadTokens, _ = strconv.Atoi(m[4])
	}
	if m[5] != "" {
		rd.CacheCreationTokens, _ = strconv.Atoi(m[5])
	}
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

	cmd := exec.CommandContext(ctx, "skaffen", "-mode", "print", "-p", prompt)
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

	// If a .venv exists, activate it before running the validation command.
	// Also propagate GOTOOLCHAIN for Go repos that need newer toolchains.
	shellCmd := validationCmd
	if _, err := os.Stat(filepath.Join(dir, ".venv", "bin", "activate")); err == nil {
		shellCmd = "source .venv/bin/activate && " + validationCmd
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		shellCmd = "export GOTOOLCHAIN=go1.23.0+auto && " + shellCmd
	}

	cmd := exec.CommandContext(ctx, "bash", "-c", shellCmd)
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

// sanitizeSessionSegment replaces hyphens with underscores so intermux's
// session name parser can split on hyphens unambiguously. Underscores are
// preserved, preventing collisions (e.g., "chi-v2" → "chi_v2", not "chiv2").
func sanitizeSessionSegment(s string) string {
	return strings.ReplaceAll(s, "-", "_")
}

// BuildTmuxSessionName creates an intermux-compatible tmux session name.
// Format: intermix-{repo}_{task}-claude
// Hyphens in repo/task IDs are replaced with underscores to avoid collisions.
func BuildTmuxSessionName(repoID, taskID string) string {
	return fmt.Sprintf("intermix-%s_%s-claude", sanitizeSessionSegment(repoID), sanitizeSessionSegment(taskID))
}

// BuildSkaffenCommand returns the command slice for running skaffen in print mode.
// The prompt is collapsed to a single line since tmux send-keys interprets
// newlines as Enter keystrokes.
func BuildSkaffenCommand(prompt string) []string {
	// Collapse multi-line prompts into a single line for tmux send-keys safety.
	collapsed := strings.Join(strings.Fields(prompt), " ")
	return []string{"skaffen", "-mode", "print", "-p", collapsed}
}

// BuildSkaffenShellCommand returns a shell-safe command string for use with
// tmux send-keys. The prompt is single-quoted with internal single quotes
// escaped as '\'' (end quote, literal quote, start quote).
func BuildSkaffenShellCommand(prompt string) string {
	collapsed := strings.Join(strings.Fields(prompt), " ")
	// Shell-escape: replace ' with '\'' for safe single-quoting
	escaped := strings.ReplaceAll(collapsed, "'", "'\\''")
	return fmt.Sprintf("skaffen -mode print -p '%s'", escaped)
}

// intermuxMapping is the JSON structure written for intermux to discover
// tmux sessions spawned by intermix.
type intermuxMapping struct {
	SessionName string `json:"session_name"`
	RepoID      string `json:"repo_id"`
	TaskID      string `json:"task_id"`
	WorkDir     string `json:"work_dir"`
	SpawnedAt   string `json:"spawned_at"`
	Agent       string `json:"agent"`
}

// SpawnSkaffenTmux launches Skaffen in a detached tmux session and writes
// an intermux mapping file to /tmp for session discovery.
// Returns the tmux session name and any error.
//
// Security: The skaffen command is executed via exec.Command with explicit
// argv (no shell interpolation). The tmux session is created empty, then
// skaffen is spawned inside it using send-keys to avoid prompt injection.
func SpawnSkaffenTmux(ctx context.Context, repoID, taskID, workDir, prompt, timeout string) (string, error) {
	sessionName := BuildTmuxSessionName(repoID, taskID)

	// Create an empty detached tmux session with the correct working directory.
	createArgs := []string{"new-session", "-d", "-s", sessionName, "-c", workDir}
	createCmd := exec.CommandContext(ctx, "tmux", createArgs...)
	var stderr bytes.Buffer
	createCmd.Stderr = &stderr
	if err := createCmd.Run(); err != nil {
		return "", fmt.Errorf("tmux new-session %s: %w: %s", sessionName, err, stderr.String())
	}

	// Spawn skaffen inside the session using send-keys.
	// The command is passed as a single shell-safe string with the prompt
	// single-quoted to prevent word splitting.
	cmdLine := BuildSkaffenShellCommand(prompt)
	sendCmd := exec.CommandContext(ctx, "tmux", "send-keys", "-t", sessionName, cmdLine, "Enter")
	var sendStderr bytes.Buffer
	sendCmd.Stderr = &sendStderr
	if err := sendCmd.Run(); err != nil {
		_ = KillTmuxSession(sessionName)
		return "", fmt.Errorf("tmux send-keys %s: %w: %s", sessionName, err, sendStderr.String())
	}

	// Write intermux mapping file for session discovery.
	mapping := intermuxMapping{
		SessionName: sessionName,
		RepoID:      repoID,
		TaskID:      taskID,
		WorkDir:     workDir,
		SpawnedAt:   time.Now().UTC().Format(time.RFC3339),
		Agent:       "skaffen",
	}
	mappingPath := fmt.Sprintf("/tmp/intermux-mapping-%s.json", sessionName)
	data, err := json.Marshal(mapping)
	if err != nil {
		return sessionName, fmt.Errorf("marshal intermux mapping: %w", err)
	}
	if err := os.WriteFile(mappingPath, data, 0644); err != nil {
		return sessionName, fmt.Errorf("write intermux mapping %s: %w", mappingPath, err)
	}

	return sessionName, nil
}

// WaitTmuxSession polls for the tmux session to end, capturing output on
// completion. If the session is still alive after timeout, it is killed.
// Returns the captured pane content, an estimated exit code (0 if session
// ended naturally, -1 on timeout), the elapsed wall-clock duration in ms,
// and any error.
func WaitTmuxSession(ctx context.Context, sessionName string, timeout time.Duration) (stdout string, exitCode int, durationMs int64, err error) {
	start := time.Now()
	deadline := start.Add(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Context cancelled — kill session and return.
			_ = KillTmuxSession(sessionName)
			elapsed := time.Since(start)
			return "", -1, elapsed.Milliseconds(), ctx.Err()
		case <-ticker.C:
			// Check if the session still exists (with per-invocation timeout).
			checkCtx, checkCancel := context.WithTimeout(ctx, 10*time.Second)
			cmd := exec.CommandContext(checkCtx, "tmux", "has-session", "-t", sessionName)
			runErr := cmd.Run()
			checkCancel()
			if runErr != nil {
				// Session no longer exists — it finished.
				elapsed := time.Since(start)
				captured, captureErr := CaptureTmuxPane(sessionName)
				if captureErr != nil {
					// Session is gone, capture may fail — that's OK.
					captured = ""
				}
				return captured, 0, elapsed.Milliseconds(), nil
			}

			// Session still alive — check timeout.
			if time.Now().After(deadline) {
				// Capture before killing.
				captured, _ := CaptureTmuxPane(sessionName)
				_ = KillTmuxSession(sessionName)
				elapsed := time.Since(start)
				return captured, -1, elapsed.Milliseconds(), fmt.Errorf("tmux session %s timed out after %v", sessionName, timeout)
			}
		}
	}
}

// CaptureTmuxPane captures the visible pane content (up to 2000 lines of
// scrollback) from the named tmux session.
func CaptureTmuxPane(sessionName string) (string, error) {
	cmd := exec.Command("tmux", "capture-pane", "-t", sessionName, "-p", "-S", "-2000")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("tmux capture-pane %s: %w: %s", sessionName, err, stderr.String())
	}
	return stdout.String(), nil
}

// KillTmuxSession kills a tmux session by name.
func KillTmuxSession(sessionName string) error {
	cmd := exec.Command("tmux", "kill-session", "-t", sessionName)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tmux kill-session %s: %w: %s", sessionName, err, stderr.String())
	}
	return nil
}
