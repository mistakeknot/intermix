package eval

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// DockerConfig holds Docker execution settings for a cell.
type DockerConfig struct {
	Image      string // Docker image tag (e.g., "intermix-swebench:py3.9")
	PythonVer  string // Python version (e.g., "3.9")
	APIKeyEnv  string // Environment variable name for API key (default: ANTHROPIC_API_KEY)
	ExtraEnvs  []string // Additional environment variables to pass through
	MemoryLimit string // Docker memory limit (e.g., "4g")
}

// pythonVersionMap maps repo/version to required Python version.
// Loaded from swebench_python_versions.json at init time.
var pythonVersionMap map[string]map[string]string

func init() {
	pythonVersionMap = make(map[string]map[string]string)

	// Try loading from embedded JSON (next to this file in the package)
	// At build time, the JSON should be in internal/eval/
	candidates := []string{
		"internal/eval/swebench_python_versions.json",
		"swebench_python_versions.json",
	}

	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		json.Unmarshal(data, &pythonVersionMap)
		break
	}

	// Hardcoded fallback for SWE-bench Lite repos if JSON not found
	if len(pythonVersionMap) == 0 {
		pythonVersionMap = defaultPythonVersions()
	}
}

// defaultPythonVersions returns the version mapping for SWE-bench Lite repos.
func defaultPythonVersions() map[string]map[string]string {
	return map[string]map[string]string{
		"django/django": {
			"1.4": "3.5", "1.5": "3.5", "1.6": "3.5", "1.7": "3.5", "1.8": "3.5",
			"1.9": "3.5", "1.10": "3.5", "1.11": "3.5", "2.0": "3.5", "2.1": "3.5",
			"2.2": "3.5", "3.0": "3.6", "3.1": "3.6", "3.2": "3.6",
			"4.0": "3.8", "4.1": "3.9", "4.2": "3.9", "5.0": "3.11",
		},
		"sympy/sympy": {
			"1.0": "3.9", "1.1": "3.9", "1.2": "3.9", "1.4": "3.9", "1.5": "3.9",
			"1.6": "3.9", "1.7": "3.9", "1.8": "3.9", "1.9": "3.9",
			"1.10": "3.9", "1.11": "3.9", "1.12": "3.9", "1.13": "3.9",
		},
		"matplotlib/matplotlib": {
			"3.0": "3.7", "3.1": "3.8", "3.2": "3.8", "3.3": "3.8", "3.4": "3.8",
			"3.5": "3.11", "3.6": "3.11", "3.7": "3.11",
		},
		"scikit-learn/scikit-learn": {
			"0.20": "3.6", "0.21": "3.6", "0.22": "3.6", "1.3": "3.9",
		},
		"pallets/flask":       {"2.0": "3.9", "2.3": "3.11"},
		"psf/requests":        {"0.14": "3.9", "2.3": "3.9", "2.4": "3.9", "2.7": "3.9", "2.10": "3.9"},
		"pytest-dev/pytest":   {"4.4": "3.9", "4.5": "3.9", "4.6": "3.9", "5.0": "3.9", "5.2": "3.9", "5.4": "3.9", "6.0": "3.9", "6.3": "3.9", "7.0": "3.9", "8.0": "3.9"},
		"sphinx-doc/sphinx":   {"3.1": "3.9", "3.2": "3.9", "3.3": "3.9", "3.4": "3.9", "3.5": "3.9", "4.0": "3.9", "5.0": "3.9", "5.1": "3.9", "7.1": "3.9"},
		"pydata/xarray":       {"0.12": "3.10"},
		"pylint-dev/pylint":   {"2.13": "3.9", "2.14": "3.9", "2.15": "3.9"},
		"mwaskom/seaborn":     {"0.12": "3.9", "0.13": "3.9"},
		"astropy/astropy":     {"1.3": "3.6", "4.3": "3.9", "5.1": "3.9", "5.2": "3.9"},
	}
}

// LookupPythonVersion returns the required Python version for a SWE-bench instance.
// Uses the repo and version fields. Returns "3.9" as default if not found.
func LookupPythonVersion(repo, version string) string {
	if versions, ok := pythonVersionMap[repo]; ok {
		if py, ok := versions[version]; ok {
			return py
		}
	}
	return "3.9" // safe default — most SWE-bench instances use 3.9
}

// DockerImageTag returns the expected Docker image tag for a Python version.
func DockerImageTag(pythonVersion string) string {
	return fmt.Sprintf("intermix-swebench:py%s", pythonVersion)
}

// DockerImageExists checks if a Docker image exists locally.
func DockerImageExists(image string) bool {
	cmd := exec.Command("docker", "image", "inspect", image)
	return cmd.Run() == nil
}

// RunCellDocker executes a single SWE-bench cell inside a Docker container.
// It handles: clone at base_commit, setup, skaffen, test_patch, validation.
// Returns RunDetails with the results.
func RunCellDocker(ctx context.Context, repo *Repo, task *Task, cfg DockerConfig, cellTimeout time.Duration) *RunDetails {
	cellID := fmt.Sprintf("%s-%s-%d", repo.ID, task.ID, time.Now().Unix())
	image := cfg.Image
	if image == "" {
		image = DockerImageTag(cfg.PythonVer)
	}

	rd := &RunDetails{
		CellID: cellID,
		Repo:   repo.ID,
		Task:   task.ID,
	}

	// Build the shell script to run inside the container
	script := buildDockerScript(repo, task)

	// Prepare docker run args
	args := []string{
		"run", "--rm",
		"--name", fmt.Sprintf("intermix-%s", cellID),
	}

	// Pass API key (required for Docker mode — skaffen can't use Claude Code proxy inside container)
	apiKeyEnv := cfg.APIKeyEnv
	if apiKeyEnv == "" {
		apiKeyEnv = "ANTHROPIC_API_KEY"
	}
	apiKey := os.Getenv(apiKeyEnv)
	if apiKey == "" {
		rd.ExitCode = -1
		rd.Stderr = fmt.Sprintf("ANTHROPIC_API_KEY not set. Docker mode requires a direct API key.\nSet it with: export ANTHROPIC_API_KEY=sk-ant-...\n(Skaffen can't use Claude Code's OAuth proxy inside a container)")
		return rd
	}
	args = append(args, "-e", fmt.Sprintf("%s=%s", apiKeyEnv, apiKey))

	// Pass extra env vars
	for _, env := range cfg.ExtraEnvs {
		args = append(args, "-e", env)
	}

	// Memory limit
	if cfg.MemoryLimit != "" {
		args = append(args, "--memory", cfg.MemoryLimit)
	}

	args = append(args, image, script)

	// Run with timeout
	timeout := cellTimeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	dockerCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(dockerCtx, "docker", args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	rd.DurationMs = time.Since(start).Milliseconds()
	rd.Stdout = truncateOutput(stdout.String(), 64*1024)
	rd.Stderr = truncateOutput(stderr.String(), 16*1024)

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			rd.ExitCode = exitErr.ExitCode()
		} else {
			rd.ExitCode = -1
		}
	}

	// Parse structured output from the container
	parseDockerOutput(rd)

	return rd
}

// buildDockerScript creates a bash script that runs the full SWE-bench pipeline
// inside the container. Output is structured with markers for parsing.
func buildDockerScript(repo *Repo, task *Task) string {
	var b strings.Builder

	commit := task.Metadata["base_commit"]
	testPatch := task.Metadata["test_patch"]

	// Clone
	b.WriteString("set -e\n")
	if commit != "" {
		b.WriteString(fmt.Sprintf("echo '::PHASE::clone'\ngit clone '%s' /workspace/repo && cd /workspace/repo && git checkout '%s'\n", repo.URL, commit))
	} else {
		b.WriteString(fmt.Sprintf("echo '::PHASE::clone'\ngit clone --depth=1 '%s' /workspace/repo && cd /workspace/repo\n", repo.URL))
	}

	// Setup
	b.WriteString("echo '::PHASE::setup'\n")
	if repo.Setup != "" {
		// Replace $PWD references for Docker context
		setup := strings.ReplaceAll(repo.Setup, "$PWD/.venv", "/workspace/repo/.venv")
		b.WriteString(fmt.Sprintf("cd /workspace/repo && %s\n", setup))
	}

	// Run Skaffen with --provider anthropic (Docker can't use Claude Code proxy)
	// Prompt is base64-encoded to avoid shell quoting issues
	b.WriteString("echo '::PHASE::skaffen'\n")
	b.WriteString("set +e\n")
	promptB64 := base64.StdEncoding.EncodeToString([]byte(task.Prompt))
	b.WriteString(fmt.Sprintf("PROMPT=$(echo '%s' | base64 -d) && cd /workspace/repo && skaffen --provider anthropic -mode print -p \"$PROMPT\"\n", promptB64))
	b.WriteString("SKAFFEN_EXIT=$?\n")
	b.WriteString("echo \"::SKAFFEN_EXIT::${SKAFFEN_EXIT}\"\n")
	b.WriteString("set -e\n")

	// Count files changed
	b.WriteString("echo '::PHASE::extract'\n")
	b.WriteString("cd /workspace/repo && git add -A && FILES_CHANGED=$(git diff --cached --name-only | wc -l) && echo \"::FILES_CHANGED::${FILES_CHANGED}\"\n")

	// Extract patch
	b.WriteString("PATCH=$(cd /workspace/repo && git diff --cached) && echo \"::PATCH_SIZE::${#PATCH}\"\n")

	// Apply test_patch if present (base64-encoded to avoid shell quoting issues)
	if testPatch != "" {
		b.WriteString("echo '::PHASE::test_patch'\n")
		b64 := base64.StdEncoding.EncodeToString([]byte(testPatch))
		b.WriteString(fmt.Sprintf("echo '%s' | base64 -d > /tmp/test.patch && cd /workspace/repo && git apply --allow-empty /tmp/test.patch 2>/dev/null || echo '::TEST_PATCH_FAILED::true'\n", b64))
	}

	// Validate
	valCmd := task.ValidationCmd
	if valCmd == "" {
		valCmd = InferValidationCmd("/workspace/repo", repo.Language)
	}
	if valCmd != "" {
		b.WriteString("echo '::PHASE::validate'\n")
		// Activate venv if it exists
		b.WriteString("cd /workspace/repo\n")
		b.WriteString("if [ -f .venv/bin/activate ]; then source .venv/bin/activate; fi\n")
		b.WriteString(fmt.Sprintf("if %s; then echo '::VALIDATION::passed'; else echo '::VALIDATION::failed'; fi\n", valCmd))
	}

	b.WriteString("echo '::PHASE::done'\n")

	return b.String()
}

// parseDockerOutput extracts structured results from Docker container stdout.
func parseDockerOutput(rd *RunDetails) {
	output := rd.Stdout + "\n" + rd.Stderr

	// Parse files changed
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "::FILES_CHANGED::") {
			fmt.Sscanf(strings.TrimPrefix(line, "::FILES_CHANGED::"), "%d", &rd.FilesChanged)
		}
		if strings.HasPrefix(line, "::VALIDATION::passed") {
			rd.ValidationPassed = true
		}
		if strings.HasPrefix(line, "::VALIDATION::failed") {
			rd.ValidationPassed = false
		}
		if strings.HasPrefix(line, "::PATCH_SIZE::") {
			var size int
			fmt.Sscanf(strings.TrimPrefix(line, "::PATCH_SIZE::"), "%d", &size)
			if size > 0 {
				rd.Patch = fmt.Sprintf("[%d bytes — extract from container]", size)
			}
		}
	}

	// Parse Skaffen token output
	ParseSkaffenTokens(rd, output)
}

// NeedsDocker returns true if this cell should run in Docker based on its
// Python version requirements. Returns the required Python version.
func NeedsDocker(task *Task) (bool, string) {
	// If no version metadata, don't need Docker
	version := task.Metadata["version"]
	repo := task.Metadata["repo"]
	if version == "" || repo == "" {
		// Try to infer from task ID (format: repo__repo-NNNN)
		return false, ""
	}

	pyVer := LookupPythonVersion(repo, version)
	// Only need Docker if the required Python differs from the host
	hostPy := getHostPythonMajorMinor()
	return pyVer != hostPy, pyVer
}

// getHostPythonMajorMinor returns the host Python's major.minor version.
func getHostPythonMajorMinor() string {
	cmd := exec.Command("python3", "-c", "import sys; print(f'{sys.version_info.major}.{sys.version_info.minor}')")
	out, err := cmd.Output()
	if err != nil {
		return "3.12" // assume 3.12 if detection fails
	}
	return strings.TrimSpace(string(out))
}

// EnsureDockerImage checks if the required Docker image exists and
// offers to build it if not.
func EnsureDockerImage(pythonVersion string) error {
	image := DockerImageTag(pythonVersion)
	if DockerImageExists(image) {
		return nil
	}
	return fmt.Errorf("Docker image %s not found. Build it with: ./docker/build-images.sh %s", image, pythonVersion)
}
