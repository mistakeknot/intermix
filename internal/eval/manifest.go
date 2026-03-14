package eval

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Manifest is the top-level intermix.yaml structure.
type Manifest struct {
	Repos    []Repo   `yaml:"repos"`
	Tasks    []Task   `yaml:"tasks"`
	Defaults Defaults `yaml:"defaults"`
}

// Repo defines a repository to evaluate against.
type Repo struct {
	ID            string     `yaml:"id"`
	URL           string     `yaml:"url"`
	Setup         string     `yaml:"setup,omitempty"`
	Language      string     `yaml:"language"`
	Complexity    string     `yaml:"complexity,omitempty"`
	SkaffenConfig SkaffenCfg `yaml:"skaffen_config,omitempty"`
}

// SkaffenCfg holds per-repo Skaffen overrides.
type SkaffenCfg struct {
	Timeout string `yaml:"timeout,omitempty"`
}

// Task defines an evaluation task (prompt + metadata).
type Task struct {
	ID            string   `yaml:"id"`
	Prompt        string   `yaml:"prompt"`
	Difficulty    string   `yaml:"difficulty,omitempty"`
	Tags          []string `yaml:"tags,omitempty"`
	Target        string   `yaml:"target,omitempty"`
	Repos         []string `yaml:"repos,omitempty"`
	ValidationCmd string   `yaml:"validation_cmd,omitempty"`
}

// Defaults holds circuit-breaker and limit defaults for the matrix.
type Defaults struct {
	Timeout                string `yaml:"timeout,omitempty"`
	MaxCells               int    `yaml:"max_cells,omitempty"`
	MaxConsecutiveFailures int    `yaml:"max_consecutive_failures,omitempty"`
	MaxDuration            string `yaml:"max_duration,omitempty"`
}

// Cell is a single (repo, task) evaluation unit produced by matrix expansion.
type Cell struct {
	RepoID string
	TaskID string
	Repo   Repo
	Task   Task
}

// ParseManifest reads an intermix.yaml file, validates required fields, and
// applies defaults for any omitted Defaults values.
func ParseManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading manifest: %w", err)
	}

	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing manifest YAML: %w", err)
	}

	// Validate required fields.
	if len(m.Repos) == 0 {
		return nil, fmt.Errorf("manifest must contain at least one repo")
	}
	if len(m.Tasks) == 0 {
		return nil, fmt.Errorf("manifest must contain at least one task")
	}
	for i, r := range m.Repos {
		if r.ID == "" {
			return nil, fmt.Errorf("repo[%d] missing required field 'id'", i)
		}
		if r.URL == "" {
			return nil, fmt.Errorf("repo[%d] (%s) missing required field 'url'", i, r.ID)
		}
	}
	for i, t := range m.Tasks {
		if t.ID == "" {
			return nil, fmt.Errorf("task[%d] missing required field 'id'", i)
		}
		if t.Prompt == "" {
			return nil, fmt.Errorf("task[%d] (%s) missing required field 'prompt'", i, t.ID)
		}
	}

	// Apply defaults.
	if m.Defaults.Timeout == "" {
		m.Defaults.Timeout = "300s"
	}
	if m.Defaults.MaxCells == 0 {
		m.Defaults.MaxCells = 100
	}
	if m.Defaults.MaxConsecutiveFailures == 0 {
		m.Defaults.MaxConsecutiveFailures = 5
	}
	if m.Defaults.MaxDuration == "" {
		m.Defaults.MaxDuration = "4h"
	}

	return &m, nil
}

// ExpandMatrix generates the (repo, task) cell matrix. Tasks that specify a
// Repos list are only paired with those repos; tasks without a Repos list are
// paired with every repo.
func ExpandMatrix(m *Manifest) []Cell {
	repoByID := make(map[string]Repo, len(m.Repos))
	for _, r := range m.Repos {
		repoByID[r.ID] = r
	}

	var cells []Cell
	for _, t := range m.Tasks {
		if len(t.Repos) > 0 {
			// Repo-specific task: only pair with listed repos.
			for _, rid := range t.Repos {
				if r, ok := repoByID[rid]; ok {
					cells = append(cells, Cell{
						RepoID: rid,
						TaskID: t.ID,
						Repo:   r,
						Task:   t,
					})
				}
			}
		} else {
			// Generic task: pair with all repos.
			for _, r := range m.Repos {
				cells = append(cells, Cell{
					RepoID: r.ID,
					TaskID: t.ID,
					Repo:   r,
					Task:   t,
				})
			}
		}
	}
	return cells
}
