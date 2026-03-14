package eval

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseManifest_ValidYAML(t *testing.T) {
	yaml := `
repos:
  - id: repo-a
    url: https://github.com/example/repo-a
    language: go
    complexity: medium
    skaffen_config:
      timeout: "600s"
  - id: repo-b
    url: https://github.com/example/repo-b
    language: python

tasks:
  - id: task-fix-bug
    prompt: "Fix the failing test in main_test.go"
    difficulty: easy
    tags: [bugfix, tests]
  - id: task-refactor
    prompt: "Refactor the handler to use dependency injection"
    difficulty: medium

defaults:
  timeout: "120s"
  max_cells: 50
`
	path := writeTempYAML(t, yaml)

	m, err := ParseManifest(path)
	if err != nil {
		t.Fatalf("ParseManifest returned error: %v", err)
	}

	// Repos
	if len(m.Repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(m.Repos))
	}
	if m.Repos[0].ID != "repo-a" {
		t.Errorf("repos[0].ID = %q, want %q", m.Repos[0].ID, "repo-a")
	}
	if m.Repos[0].Language != "go" {
		t.Errorf("repos[0].Language = %q, want %q", m.Repos[0].Language, "go")
	}
	if m.Repos[0].SkaffenConfig.Timeout != "600s" {
		t.Errorf("repos[0].SkaffenConfig.Timeout = %q, want %q", m.Repos[0].SkaffenConfig.Timeout, "600s")
	}
	if m.Repos[1].ID != "repo-b" {
		t.Errorf("repos[1].ID = %q, want %q", m.Repos[1].ID, "repo-b")
	}

	// Tasks
	if len(m.Tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(m.Tasks))
	}
	if m.Tasks[0].ID != "task-fix-bug" {
		t.Errorf("tasks[0].ID = %q, want %q", m.Tasks[0].ID, "task-fix-bug")
	}
	if m.Tasks[0].Prompt != "Fix the failing test in main_test.go" {
		t.Errorf("tasks[0].Prompt = %q, unexpected", m.Tasks[0].Prompt)
	}
	if len(m.Tasks[0].Tags) != 2 {
		t.Errorf("tasks[0].Tags length = %d, want 2", len(m.Tasks[0].Tags))
	}

	// Explicit defaults
	if m.Defaults.Timeout != "120s" {
		t.Errorf("defaults.Timeout = %q, want %q", m.Defaults.Timeout, "120s")
	}
	if m.Defaults.MaxCells != 50 {
		t.Errorf("defaults.MaxCells = %d, want 50", m.Defaults.MaxCells)
	}

	// Applied defaults (not set in YAML)
	if m.Defaults.MaxConsecutiveFailures != 5 {
		t.Errorf("defaults.MaxConsecutiveFailures = %d, want 5 (default)", m.Defaults.MaxConsecutiveFailures)
	}
	if m.Defaults.MaxDuration != "4h" {
		t.Errorf("defaults.MaxDuration = %q, want %q (default)", m.Defaults.MaxDuration, "4h")
	}
}

func TestParseManifest_MissingRequired(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "empty repos",
			yaml: `
repos: []
tasks:
  - id: t1
    prompt: "do something"
`,
		},
		{
			name: "empty tasks",
			yaml: `
repos:
  - id: r1
    url: https://example.com/r1
tasks: []
`,
		},
		{
			name: "repo missing id",
			yaml: `
repos:
  - url: https://example.com/r1
tasks:
  - id: t1
    prompt: "do something"
`,
		},
		{
			name: "repo missing url",
			yaml: `
repos:
  - id: r1
tasks:
  - id: t1
    prompt: "do something"
`,
		},
		{
			name: "task missing id",
			yaml: `
repos:
  - id: r1
    url: https://example.com/r1
tasks:
  - prompt: "do something"
`,
		},
		{
			name: "task missing prompt",
			yaml: `
repos:
  - id: r1
    url: https://example.com/r1
tasks:
  - id: t1
`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempYAML(t, tc.yaml)
			_, err := ParseManifest(path)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			t.Logf("got expected error: %v", err)
		})
	}
}

func TestExpandMatrix(t *testing.T) {
	m := &Manifest{
		Repos: []Repo{
			{ID: "repo-a", URL: "https://example.com/a"},
			{ID: "repo-b", URL: "https://example.com/b"},
		},
		Tasks: []Task{
			{ID: "generic-task", Prompt: "do something everywhere"},
			{ID: "specific-task", Prompt: "do something in repo-a only", Repos: []string{"repo-a"}},
		},
	}

	cells := ExpandMatrix(m)

	// generic-task applies to both repos (2 cells) + specific-task applies to repo-a only (1 cell) = 3
	if len(cells) != 3 {
		t.Fatalf("expected 3 cells, got %d", len(cells))
	}

	// Verify cell contents.
	type pair struct{ repo, task string }
	got := make(map[pair]bool)
	for _, c := range cells {
		got[pair{c.RepoID, c.TaskID}] = true
		// Verify embedded structs are populated.
		if c.Repo.URL == "" {
			t.Errorf("cell (%s, %s) has empty Repo.URL", c.RepoID, c.TaskID)
		}
		if c.Task.Prompt == "" {
			t.Errorf("cell (%s, %s) has empty Task.Prompt", c.RepoID, c.TaskID)
		}
	}

	expected := []pair{
		{"repo-a", "generic-task"},
		{"repo-b", "generic-task"},
		{"repo-a", "specific-task"},
	}
	for _, e := range expected {
		if !got[e] {
			t.Errorf("missing expected cell (%s, %s)", e.repo, e.task)
		}
	}

	// specific-task should NOT pair with repo-b.
	if got[pair{"repo-b", "specific-task"}] {
		t.Errorf("unexpected cell (repo-b, specific-task) — repo-specific task leaked")
	}
}

// writeTempYAML writes content to a temporary YAML file and returns the path.
func writeTempYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "intermix.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing temp YAML: %v", err)
	}
	return path
}
