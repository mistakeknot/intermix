package eval

import (
	"testing"
)

func TestBuildBatchCells(t *testing.T) {
	manifest := &Manifest{
		Repos: []Repo{
			{ID: "chi", URL: "https://github.com/go-chi/chi", Setup: "go mod download", Language: "go"},
			{ID: "zod", URL: "https://github.com/colinhacks/zod", Setup: "npm install", Language: "typescript"},
		},
		Tasks: []Task{
			{ID: "add-test", Prompt: "Write a test", Difficulty: "easy"},
			{ID: "refactor-extract", Prompt: "Refactor a function", Difficulty: "medium"},
		},
	}

	cells := BuildBatchCells(manifest, nil) // nil = no filter, all combos
	if len(cells) != 4 {
		t.Errorf("expected 4 cells, got %d", len(cells))
	}

	// Verify cell IDs are unique
	seen := make(map[string]bool)
	for _, c := range cells {
		if seen[c.ID()] {
			t.Errorf("duplicate cell ID: %s", c.ID())
		}
		seen[c.ID()] = true
	}
}

func TestBuildBatchCellsWithRepoFilter(t *testing.T) {
	manifest := &Manifest{
		Repos: []Repo{
			{ID: "chi", URL: "https://github.com/go-chi/chi", Setup: "go mod download", Language: "go"},
			{ID: "zod", URL: "https://github.com/colinhacks/zod", Setup: "npm install", Language: "typescript"},
		},
		Tasks: []Task{
			{ID: "add-test", Prompt: "Write a test", Difficulty: "easy"},
		},
	}

	filter := []string{"chi"}
	cells := BuildBatchCells(manifest, filter)
	if len(cells) != 1 {
		t.Errorf("expected 1 cell, got %d", len(cells))
	}
	if len(cells) > 0 && cells[0].Repo.ID != "chi" {
		t.Errorf("expected chi, got %s", cells[0].Repo.ID)
	}
}

func TestBuildBatchCellsRespectsTaskRepos(t *testing.T) {
	manifest := &Manifest{
		Repos: []Repo{
			{ID: "chi", URL: "https://github.com/go-chi/chi", Language: "go"},
			{ID: "zod", URL: "https://github.com/colinhacks/zod", Language: "typescript"},
			{ID: "click", URL: "https://github.com/pallets/click", Language: "python"},
		},
		Tasks: []Task{
			{ID: "generic-task", Prompt: "Do something generic", Difficulty: "easy"},
			{ID: "go-only-task", Prompt: "Do something Go-specific", Difficulty: "medium", Repos: []string{"chi"}},
		},
	}

	cells := BuildBatchCells(manifest, nil)

	// generic-task pairs with all 3 repos = 3 cells
	// go-only-task pairs only with chi = 1 cell
	// Total = 4
	if len(cells) != 4 {
		t.Errorf("expected 4 cells, got %d", len(cells))
	}

	// Verify go-only-task only paired with chi
	for _, c := range cells {
		if c.Task.ID == "go-only-task" && c.Repo.ID != "chi" {
			t.Errorf("go-only-task should only pair with chi, got %s", c.Repo.ID)
		}
	}

	// Count cells per task
	taskCounts := make(map[string]int)
	for _, c := range cells {
		taskCounts[c.Task.ID]++
	}
	if taskCounts["generic-task"] != 3 {
		t.Errorf("expected 3 generic-task cells, got %d", taskCounts["generic-task"])
	}
	if taskCounts["go-only-task"] != 1 {
		t.Errorf("expected 1 go-only-task cell, got %d", taskCounts["go-only-task"])
	}
}
