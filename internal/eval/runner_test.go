package eval

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// runCmd is a test helper that runs a command in the given directory and fails
// the test on non-zero exit.
func runCmd(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
	}
}

func TestCloneRepo(t *testing.T) {
	// Skip if git is not available.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available, skipping TestCloneRepo")
	}

	// Create a local bare-ish repo to clone from.
	srcDir := t.TempDir()
	runCmd(t, srcDir, "git", "init")
	runCmd(t, srcDir, "git", "config", "user.email", "test@test.com")
	runCmd(t, srcDir, "git", "config", "user.name", "Test")

	// Write a file and commit.
	sentinel := filepath.Join(srcDir, "hello.txt")
	if err := os.WriteFile(sentinel, []byte("hello from test repo"), 0644); err != nil {
		t.Fatalf("writing sentinel file: %v", err)
	}
	runCmd(t, srcDir, "git", "add", "hello.txt")
	runCmd(t, srcDir, "git", "commit", "-m", "initial commit")

	// Clone into a new temp dir.
	destDir := filepath.Join(t.TempDir(), "clone")
	if err := CloneRepo(srcDir, destDir); err != nil {
		t.Fatalf("CloneRepo: %v", err)
	}

	// Verify the sentinel file exists in the clone.
	clonedFile := filepath.Join(destDir, "hello.txt")
	data, err := os.ReadFile(clonedFile)
	if err != nil {
		t.Fatalf("reading cloned file: %v", err)
	}
	if string(data) != "hello from test repo" {
		t.Errorf("cloned file content = %q, want %q", string(data), "hello from test repo")
	}

	// Verify it is a git repo (has .git dir).
	if _, err := os.Stat(filepath.Join(destDir, ".git")); err != nil {
		t.Errorf("expected .git directory in clone, got error: %v", err)
	}
}

func TestRunValidation_Pass(t *testing.T) {
	dir := t.TempDir()
	result := RunValidation(dir, "true")
	if !result.Passed {
		t.Errorf("expected validation to pass with 'true' command")
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", result.ExitCode)
	}
}

func TestRunValidation_Fail(t *testing.T) {
	dir := t.TempDir()
	result := RunValidation(dir, "false")
	if result.Passed {
		t.Errorf("expected validation to fail with 'false' command")
	}
	if result.ExitCode == 0 {
		t.Errorf("exit code = 0, want non-zero for 'false' command")
	}
}

func TestRunValidation_Empty(t *testing.T) {
	dir := t.TempDir()
	result := RunValidation(dir, "")
	if !result.Passed {
		t.Errorf("expected empty validation command to pass")
	}
	if result.Output != "no validation command configured" {
		t.Errorf("output = %q, want %q", result.Output, "no validation command configured")
	}
}

func TestInferValidationCmd(t *testing.T) {
	tests := []struct {
		language string
		want     string
	}{
		{"go", "go test ./..."},
		{"golang", "go test ./..."},
		{"python", "pytest"},
		{"py", "pytest"},
		{"rust", "cargo test"},
		{"rs", "cargo test"},
		{"ts", "npm test"},
		{"typescript", "npm test"},
		{"js", "npm test"},
		{"javascript", "npm test"},
		{"unknown", ""},
	}

	dir := t.TempDir()
	for _, tc := range tests {
		t.Run(tc.language, func(t *testing.T) {
			got := InferValidationCmd(dir, tc.language)
			if got != tc.want {
				t.Errorf("InferValidationCmd(%q) = %q, want %q", tc.language, got, tc.want)
			}
		})
	}
}

func TestTruncateOutput(t *testing.T) {
	short := "hello"
	if got := truncateOutput(short, 100); got != short {
		t.Errorf("short string should not be truncated: got %q", got)
	}

	long := "abcdefghijklmnopqrstuvwxyz"
	got := truncateOutput(long, 10)
	if len(got) > 30 { // 10 bytes + truncation notice
		t.Errorf("truncated output too long: %d bytes", len(got))
	}
	if got == long {
		t.Errorf("expected string to be truncated")
	}
}

func TestCountFilesChanged(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	runCmd(t, dir, "git", "init")
	runCmd(t, dir, "git", "config", "user.email", "test@test.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")

	// No changes yet (empty repo, no commits).
	if n := countFilesChanged(dir); n != 0 {
		t.Errorf("expected 0 changed files in fresh repo, got %d", n)
	}

	// Create a file — should show as untracked.
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	if n := countFilesChanged(dir); n != 1 {
		t.Errorf("expected 1 changed file after adding untracked file, got %d", n)
	}
}

func TestRunSetup(t *testing.T) {
	dir := t.TempDir()

	// Empty setup should be a no-op.
	if err := RunSetup(dir, ""); err != nil {
		t.Errorf("empty setup should succeed: %v", err)
	}

	// Successful setup.
	if err := RunSetup(dir, "echo setup-ok"); err != nil {
		t.Errorf("echo setup should succeed: %v", err)
	}

	// Failing setup.
	if err := RunSetup(dir, "exit 1"); err == nil {
		t.Errorf("expected error from failing setup command")
	}
}

func TestBuildTmuxSessionName(t *testing.T) {
	tests := []struct {
		name   string
		repoID string
		taskID string
		want   string
	}{
		{
			name:   "simple IDs",
			repoID: "chi",
			taskID: "refactor",
			want:   "intermix-chi_refactor-claude",
		},
		{
			name:   "hyphenated repo ID",
			repoID: "my-repo",
			taskID: "task1",
			want:   "intermix-my_repo_task1-claude",
		},
		{
			name:   "hyphenated task ID",
			repoID: "zod",
			taskID: "add-tests",
			want:   "intermix-zod_add_tests-claude",
		},
		{
			name:   "both hyphenated",
			repoID: "my-cool-repo",
			taskID: "fix-bug-123",
			want:   "intermix-my_cool_repo_fix_bug_123-claude",
		},
		{
			name:   "no hyphens",
			repoID: "click",
			taskID: "docs",
			want:   "intermix-click_docs-claude",
		},
		{
			name:   "collision prevention",
			repoID: "chi-v2",
			taskID: "test",
			want:   "intermix-chi_v2_test-claude",
		},
		{
			name:   "empty strings",
			repoID: "",
			taskID: "",
			want:   "intermix-_-claude",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildTmuxSessionName(tc.repoID, tc.taskID)
			if got != tc.want {
				t.Errorf("BuildTmuxSessionName(%q, %q) = %q, want %q", tc.repoID, tc.taskID, got, tc.want)
			}
		})
	}
}

func TestBuildSkaffenCommand(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   []string
	}{
		{
			name:   "basic command",
			prompt: "Refactor the config parser",
			want:   []string{"skaffen", "-mode", "print", "-p", "Refactor the config parser"},
		},
		{
			name:   "empty prompt",
			prompt: "",
			want:   []string{"skaffen", "-mode", "print", "-p", ""},
		},
		{
			name:   "prompt with special characters",
			prompt: "Fix bug #42: handle 'quoted' strings & <angle> brackets",
			want:   []string{"skaffen", "-mode", "print", "-p", "Fix bug #42: handle 'quoted' strings & <angle> brackets"},
		},
		{
			name:   "multi-line prompt collapses to single line",
			prompt: "Find a function\nthat is too long.\nRefactor it.",
			want:   []string{"skaffen", "-mode", "print", "-p", "Find a function that is too long. Refactor it."},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildSkaffenCommand(tc.prompt)
			if len(got) != len(tc.want) {
				t.Fatalf("BuildSkaffenCommand() returned %d elements, want %d", len(got), len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("BuildSkaffenCommand()[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestParseSkaffenTokens(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		wantIn  int
		wantOut int
		wantCR  int
		wantCC  int
	}{
		{"basic", "[1 turns, 37 in / 9186 out tokens]", 37, 9186, 0, 0},
		{"with cache", "[1 turns, 15 in / 6 out tokens, 3400 cache_read / 1200 cache_create]", 15, 6, 3400, 1200},
		{"embedded", "text\n[2 turns, 100 in / 500 out tokens]\nmore", 100, 500, 0, 0},
		{"no match", "no token info here", 0, 0, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rd := &RunDetails{}
			ParseSkaffenTokens(rd, tc.output)
			if rd.InputTokens != tc.wantIn {
				t.Errorf("InputTokens = %d, want %d", rd.InputTokens, tc.wantIn)
			}
			if rd.OutputTokens != tc.wantOut {
				t.Errorf("OutputTokens = %d, want %d", rd.OutputTokens, tc.wantOut)
			}
			if rd.CacheReadTokens != tc.wantCR {
				t.Errorf("CacheReadTokens = %d, want %d", rd.CacheReadTokens, tc.wantCR)
			}
			if rd.CacheCreationTokens != tc.wantCC {
				t.Errorf("CacheCreationTokens = %d, want %d", rd.CacheCreationTokens, tc.wantCC)
			}
		})
	}
}
