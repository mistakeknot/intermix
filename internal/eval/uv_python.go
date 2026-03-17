package eval

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// UvPythonAvailable returns true if uv is installed and supports python management.
func UvPythonAvailable() bool {
	cmd := exec.Command("uv", "python", "list")
	return cmd.Run() == nil
}

// UvPythonInstall downloads and installs a Python version via uv.
// Unlike pyenv, this downloads prebuilt standalones (~30 seconds, no compilation).
// Returns the path to the installed Python binary.
func UvPythonInstall(version string) (string, error) {
	cmd := exec.Command("uv", "python", "install", version)
	cmd.Stdout = os.Stderr // show progress
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("uv python install %s: %w", version, err)
	}

	// Find the installed binary path
	return UvPythonFind(version)
}

// UvPythonFind returns the path to a specific Python version managed by uv.
// Returns empty string and error if not found.
func UvPythonFind(version string) (string, error) {
	cmd := exec.Command("uv", "python", "find", version)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("uv python find %s: %w: %s", version, err, stderr.String())
	}
	path := strings.TrimSpace(stdout.String())
	if path == "" {
		return "", fmt.Errorf("uv python find %s: empty result", version)
	}
	return path, nil
}

// UvPythonEnv returns environment variables that configure a specific Python
// version installed via uv. These use the same format as PyenvEnv so they
// can be consumed by RunSetupWithEnv without changes.
//
// The key difference from pyenv: we store the python binary path in
// UV_PYTHON_PATH (a custom var) so RunSetupWithEnv can use it with
// `uv venv --python <path>`. We also set PYENV_VERSION as a version hint
// so the existing setup-rewrite logic in RunSetupWithEnv works unchanged.
func UvPythonEnv(version, pythonPath string) []string {
	return []string{
		fmt.Sprintf("UV_PYTHON_PATH=%s", pythonPath),
		fmt.Sprintf("PYENV_VERSION=%s", version),
	}
}
