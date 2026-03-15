package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// SWEBenchInstance represents a single SWE-bench task instance.
type SWEBenchInstance struct {
	InstanceID       string `json:"instance_id"`
	Repo             string `json:"repo"`             // e.g. "django/django"
	BaseCommit       string `json:"base_commit"`      // commit SHA to checkout
	ProblemStatement string `json:"problem_statement"` // issue description
	HintsText        string `json:"hints_text"`       // optional hints
	TestPatch        string `json:"test_patch"`       // patch that adds/modifies tests
	Patch            string `json:"patch"`            // gold patch (for comparison only)
	FailToPass       string `json:"FAIL_TO_PASS"`     // JSON array of test IDs that should pass after fix
	PassToPass       string `json:"PASS_TO_PASS"`     // JSON array of test IDs that must still pass
	Version          string `json:"version,omitempty"`
}

// LoadSWEBenchDataset reads a SWE-bench JSONL file and returns instances.
// If instanceIDs is non-nil, only matching instances are returned.
func LoadSWEBenchDataset(path string, instanceIDs []string) ([]SWEBenchInstance, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read swe-bench dataset: %w", err)
	}

	filterSet := make(map[string]bool)
	for _, id := range instanceIDs {
		filterSet[id] = true
	}

	var instances []SWEBenchInstance
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}

		var inst SWEBenchInstance
		if err := json.Unmarshal([]byte(line), &inst); err != nil {
			continue // skip malformed lines
		}

		if len(filterSet) > 0 && !filterSet[inst.InstanceID] {
			continue
		}

		instances = append(instances, inst)
	}

	return instances, nil
}

// SWEBenchToManifest converts SWE-bench instances to an intermix Manifest.
// Each instance becomes a repo+task pair. The repo URL is constructed from
// the repo field (e.g., "django/django" → "https://github.com/django/django").
func SWEBenchToManifest(instances []SWEBenchInstance) *Manifest {
	// Deduplicate repos
	repoMap := make(map[string]*Repo)
	for _, inst := range instances {
		repoID := strings.ReplaceAll(inst.Repo, "/", "__") // django/django → django__django
		if _, ok := repoMap[repoID]; !ok {
			lang := inferLanguageFromRepo(inst.Repo)
			repoMap[repoID] = &Repo{
				ID:       repoID,
				URL:      "https://github.com/" + inst.Repo,
				Language: lang,
				Setup:    inferSetupFromLanguage(lang),
			}
		}
	}

	// Build repos list
	var repos []Repo
	for _, r := range repoMap {
		repos = append(repos, *r)
	}

	// Each instance becomes a task, keyed to its repo
	var tasks []Task
	for _, inst := range instances {
		repoID := strings.ReplaceAll(inst.Repo, "/", "__")
		prompt := buildSWEBenchPrompt(inst)
		tasks = append(tasks, Task{
			ID:     inst.InstanceID,
			Prompt: prompt,
			Repos:  []string{repoID},
			Difficulty: "swe-bench",
			ValidationCmd: buildSWEBenchValidationCmd(inst),
			Metadata: map[string]string{
				"base_commit": inst.BaseCommit,
				"test_patch":  inst.TestPatch,
				"fail_to_pass": inst.FailToPass,
				"pass_to_pass": inst.PassToPass,
			},
		})
	}

	return &Manifest{
		Repos: repos,
		Tasks: tasks,
		Defaults: Defaults{
			Timeout:               "600s",
			MaxCells:              len(instances),
			MaxConsecutiveFailures: len(instances), // don't circuit-break on bench runs
		},
	}
}

// buildSWEBenchPrompt constructs the agent prompt from a SWE-bench instance.
func buildSWEBenchPrompt(inst SWEBenchInstance) string {
	var b strings.Builder
	b.WriteString("Fix the following issue in this repository.\n\n")
	b.WriteString("## Issue\n\n")
	b.WriteString(inst.ProblemStatement)
	if inst.HintsText != "" {
		b.WriteString("\n\n## Hints\n\n")
		b.WriteString(inst.HintsText)
	}
	b.WriteString("\n\nMake the minimal code changes needed to fix the issue. ")
	b.WriteString("All existing tests must still pass after your fix.")
	return b.String()
}

// buildSWEBenchValidationCmd builds a validation command that applies the
// test patch and runs the failing tests. Returns empty string if no test
// patch is available (falls back to inferred validation).
func buildSWEBenchValidationCmd(inst SWEBenchInstance) string {
	if inst.TestPatch == "" {
		return ""
	}
	// The test patch is applied, then we run the test suite
	// For Python repos (most of SWE-bench), this is pytest
	lang := inferLanguageFromRepo(inst.Repo)
	testCmd := "pytest -x -q"
	switch lang {
	case "python":
		testCmd = "pytest -x -q"
	case "javascript", "typescript":
		testCmd = "npm test"
	case "go":
		testCmd = "go test ./..."
	}
	return testCmd
}

// inferLanguageFromRepo guesses the language from a GitHub repo path.
func inferLanguageFromRepo(repo string) string {
	// SWE-bench Lite is predominantly Python repos
	pythonRepos := map[string]bool{
		"django/django": true, "pallets/flask": true, "psf/requests": true,
		"sympy/sympy": true, "scikit-learn/scikit-learn": true,
		"matplotlib/matplotlib": true, "pydata/xarray": true,
		"pylint-dev/pylint": true, "pytest-dev/pytest": true,
		"sphinx-doc/sphinx": true, "astropy/astropy": true,
	}
	if pythonRepos[repo] {
		return "python"
	}
	// Default to python for SWE-bench (it's 95%+ Python)
	return "python"
}

// inferSetupFromLanguage returns a setup command for the given language.
func inferSetupFromLanguage(lang string) string {
	switch lang {
	case "python":
		return "uv venv .venv && uv pip install -e '.[dev,test]' 2>/dev/null || uv pip install -e . 2>/dev/null || pip install -e . 2>/dev/null"
	case "javascript", "typescript":
		return "npm install"
	case "go":
		return "export GOTOOLCHAIN=go1.23.0+auto && go mod download"
	default:
		return ""
	}
}
