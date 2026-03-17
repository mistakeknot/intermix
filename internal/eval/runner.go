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
	Patch                string `json:"patch,omitempty"`
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

// CloneRepo performs a git clone of url into destDir. If commit is non-empty,
// checks out that specific commit (required for SWE-bench). Uses shallow clone
// when no commit is specified, full clone when a commit is needed.
func CloneRepo(url, destDir string) error {
	return CloneRepoAt(url, destDir, "")
}

// CloneRepoAt clones a repo and optionally checks out a specific commit.
func CloneRepoAt(url, destDir, commit string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	cloneArgs := []string{"clone"}
	if commit == "" {
		cloneArgs = append(cloneArgs, "--depth=1")
	}
	cloneArgs = append(cloneArgs, url, destDir)

	cmd := exec.CommandContext(ctx, "git", cloneArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone %s: %w: %s", url, err, stderr.String())
	}

	if commit != "" {
		checkoutCmd := exec.CommandContext(ctx, "git", "checkout", commit)
		checkoutCmd.Dir = destDir
		var coStderr bytes.Buffer
		checkoutCmd.Stderr = &coStderr
		if err := checkoutCmd.Run(); err != nil {
			return fmt.Errorf("git checkout %s: %w: %s", commit, err, coStderr.String())
		}
	}

	return nil
}

// RunSetup executes a setup command in the given directory with a 300s timeout.
func RunSetup(dir, setupCmd string) error {
	return RunSetupWithEnv(dir, setupCmd, nil)
}

// RunSetupWithEnv executes a setup command with optional extra environment variables.
// Extra env vars (e.g., from pyenv) are prepended to the inherited environment.
func RunSetupWithEnv(dir, setupCmd string, extraEnv []string) error {
	if setupCmd == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	// If pyenv env vars are set, rewrite the setup command to use the correct
	// Python binary for venv creation and pip installation.
	// uv ignores PATH for Python discovery and doesn't support Python < 3.8,
	// so we must handle both cases.
	shellCmd := setupCmd
	if len(extraEnv) > 0 {
		// Find the Python binary — check UV_PYTHON_PATH first (uv python),
		// then fall back to pyenv PATH entry.
		pyBin := uvPythonFromEnv(extraEnv)
		if pyBin == "" {
			pyBin = pyenvPythonFromEnv(extraEnv)
		}
		if pyBin != "" {
			// Determine if uv supports this Python version (3.8+)
			var pyVersion string
			for _, e := range extraEnv {
				if strings.HasPrefix(e, "PYENV_VERSION=") {
					pyVersion = strings.TrimPrefix(e, "PYENV_VERSION=")
				}
			}
			uvSupported := !strings.HasPrefix(pyVersion, "3.6") && !strings.HasPrefix(pyVersion, "3.7")

			if strings.Contains(shellCmd, "uv venv .venv") {
				if uvSupported {
					shellCmd = strings.Replace(shellCmd, "uv venv .venv",
						fmt.Sprintf("uv venv .venv --python %s", pyBin), 1)
				} else {
					// Python < 3.8: uv doesn't work at all, use native venv + pip
					shellCmd = strings.Replace(shellCmd, "uv venv .venv",
						fmt.Sprintf("%s -m venv .venv", pyBin), 1)
				}
			}

			// For Python < 3.8, replace all "VIRTUAL_ENV=... uv pip install" with .venv/bin/pip
			if !uvSupported {
				shellCmd = strings.ReplaceAll(shellCmd,
					"VIRTUAL_ENV=$PWD/.venv uv pip install",
					".venv/bin/pip install")
			}
		}
	}

	// Pre-install setuptools into the venv BEFORE the main setup command.
	// Old repos (pytest 4.x, sphinx 3.x, scikit-learn 0.x) import pkg_resources
	// at setup.py/build time, so setuptools must be present before `pip install -e .`.
	//
	// Also add --no-build-isolation to editable installs so the build backend
	// can see the venv's setuptools. Without this, uv creates a temporary isolated
	// build env that lacks setuptools even if we pre-installed it.
	if strings.Contains(shellCmd, "uv pip install -e") || strings.Contains(shellCmd, "pip install -e") {
		// Insert setuptools install before the first editable install
		for _, marker := range []string{"VIRTUAL_ENV=$PWD/.venv uv pip install -e", ".venv/bin/pip install -e"} {
			if idx := strings.Index(shellCmd, marker); idx != -1 {
				preInstall := "VIRTUAL_ENV=$PWD/.venv uv pip install 'setuptools<71' pip wheel 2>/dev/null; "
				shellCmd = shellCmd[:idx] + preInstall + shellCmd[idx:]
				break
			}
		}
		// Add --no-build-isolation to uv pip install -e so the build can see setuptools
		shellCmd = strings.ReplaceAll(shellCmd,
			"uv pip install -e",
			"uv pip install --no-build-isolation -e")
	}

	// Append auto-install of pytest + setuptools after the main setup.
	// setuptools must be re-installed AFTER editable install because some repos
	// (pytest 4.x, sphinx 3.x) import pkg_resources at runtime and the editable
	// install may have removed or not included it.
	shellCmd += " && (VIRTUAL_ENV=$PWD/.venv uv pip install pytest 'setuptools<71' 2>/dev/null || .venv/bin/pip install pytest 'setuptools<71' 2>/dev/null || true)"

	cmd := exec.CommandContext(ctx, "bash", "-c", shellCmd)
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
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

// RunValidation executes a validation command in dir with a 300s timeout.
func RunValidation(dir, validationCmd string) ValidationResult {
	return RunValidationWithEnv(dir, validationCmd, nil)
}

// RunValidationWithEnv executes a validation command with optional extra environment variables.
func RunValidationWithEnv(dir, validationCmd string, extraEnv []string) ValidationResult {
	if validationCmd == "" {
		return ValidationResult{Passed: true, Output: "no validation command configured"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
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
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}

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
		return "pytest -x -q"
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

// InferTargetedValidationCmd detects changed files via git and builds a
// validation command that only runs tests related to those files.
// Returns empty string if no targeted command can be inferred (caller
// should fall back to the full validation command).
func InferTargetedValidationCmd(dir, language string) string {
	// Get changed files
	cmd := exec.Command("git", "diff", "--name-only", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return ""
	}

	var changedFiles []string
	for _, line := range bytes.Split(out, []byte("\n")) {
		f := strings.TrimSpace(string(line))
		if f != "" {
			changedFiles = append(changedFiles, f)
		}
	}
	if len(changedFiles) == 0 {
		return ""
	}

	switch strings.ToLower(language) {
	case "go", "golang":
		// Find unique Go packages containing changed files
		pkgs := make(map[string]bool)
		for _, f := range changedFiles {
			if strings.HasSuffix(f, ".go") {
				pkg := "./" + filepath.Dir(f)
				pkgs[pkg] = true
			}
		}
		if len(pkgs) == 0 {
			return ""
		}
		pkgList := make([]string, 0, len(pkgs))
		for p := range pkgs {
			pkgList = append(pkgList, p)
		}
		return "go test " + strings.Join(pkgList, " ")

	case "python", "py":
		// Find test files matching changed files
		var testFiles []string
		for _, f := range changedFiles {
			if !strings.HasSuffix(f, ".py") {
				continue
			}
			base := filepath.Base(f)
			dir := filepath.Dir(f)
			// If it's already a test file, use it directly
			if strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") {
				testFiles = append(testFiles, f)
				continue
			}
			// Look for corresponding test file
			candidates := []string{
				filepath.Join(dir, "test_"+base),
				filepath.Join("tests", "test_"+base),
				filepath.Join(dir, strings.TrimSuffix(base, ".py")+"_test.py"),
			}
			for _, c := range candidates {
				if _, err := os.Stat(filepath.Join(dir, "..", c)); err == nil {
					testFiles = append(testFiles, c)
					break
				}
			}
		}
		if len(testFiles) == 0 {
			return ""
		}
		return "pytest -x -q " + strings.Join(testFiles, " ")

	case "ts", "typescript", "js", "javascript":
		// Find test files matching changed files
		var testFiles []string
		for _, f := range changedFiles {
			if !strings.HasSuffix(f, ".ts") && !strings.HasSuffix(f, ".js") {
				continue
			}
			base := filepath.Base(f)
			// If it's already a test file, use it
			if strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") {
				testFiles = append(testFiles, f)
			}
		}
		if len(testFiles) == 0 {
			return ""
		}
		// npm test typically runs all tests; for targeted, use the test runner directly
		return "npx vitest run " + strings.Join(testFiles, " ")

	default:
		return ""
	}
}

// ApplyTestPatch applies a SWE-bench test patch to the working directory.
// The test patch adds or modifies tests that verify the fix. It is applied
// after Skaffen makes its changes but before validation runs.
// Returns nil if patchContent is empty (no test patch to apply).
func ApplyTestPatch(dir, patchContent string) error {
	if patchContent == "" {
		return nil
	}

	// Write patch to temp file
	patchFile := filepath.Join(dir, ".swebench-test.patch")
	if err := os.WriteFile(patchFile, []byte(patchContent), 0644); err != nil {
		return fmt.Errorf("write test patch: %w", err)
	}
	defer os.Remove(patchFile)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Try --3way first for better merge handling when Skaffen modified the
	// same files as the test patch. Falls back to plain apply on failure.
	cmd := exec.CommandContext(ctx, "git", "apply", "--3way", "--allow-empty", patchFile)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// --3way can fail if there's no common ancestor; fall back to plain apply
		stderr.Reset()
		cmd2 := exec.CommandContext(ctx, "git", "apply", "--allow-empty", patchFile)
		cmd2.Dir = dir
		cmd2.Stderr = &stderr
		if err2 := cmd2.Run(); err2 != nil {
			return fmt.Errorf("git apply test patch: %w: %s", err2, stderr.String())
		}
	}
	return nil
}

// uvPythonFromEnv extracts the uv-managed Python binary path from extra env vars.
// Returns empty string if UV_PYTHON_PATH is not set.
func uvPythonFromEnv(extraEnv []string) string {
	for _, e := range extraEnv {
		if strings.HasPrefix(e, "UV_PYTHON_PATH=") {
			return strings.TrimPrefix(e, "UV_PYTHON_PATH=")
		}
	}
	return ""
}

// pyenvPythonFromEnv extracts the pyenv Python binary path from extra env vars.
// Returns empty string if not found.
func pyenvPythonFromEnv(extraEnv []string) string {
	for _, e := range extraEnv {
		if strings.HasPrefix(e, "PATH=") {
			pathVal := strings.TrimPrefix(e, "PATH=")
			parts := strings.SplitN(pathVal, ":", 2)
			if len(parts) > 0 {
				candidate := filepath.Join(parts[0], "python3")
				if _, err := os.Stat(candidate); err == nil {
					return candidate
				}
			}
		}
	}
	return ""
}

// ExtractPatch generates a unified diff of all changes in the working directory.
// Returns the patch as a string. Used for SWE-bench submission format.
func ExtractPatch(dir string) (string, error) {
	// Stage all changes first so we capture new files too
	addCmd := exec.Command("git", "add", "-A")
	addCmd.Dir = dir
	addCmd.Run() // best-effort

	cmd := exec.Command("git", "diff", "--cached")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff --cached: %w", err)
	}
	return string(out), nil
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
