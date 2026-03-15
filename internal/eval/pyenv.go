package eval

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// PyenvAvailable returns true if pyenv is installed and functional.
func PyenvAvailable() bool {
	pyenvRoot := os.Getenv("PYENV_ROOT")
	if pyenvRoot == "" {
		pyenvRoot = filepath.Join(os.Getenv("HOME"), ".pyenv")
	}
	_, err := os.Stat(filepath.Join(pyenvRoot, "bin", "pyenv"))
	return err == nil
}

// PyenvVersionInstalled checks if a specific Python version is installed via pyenv.
func PyenvVersionInstalled(version string) bool {
	pyenvBin := pyenvBinPath()
	if pyenvBin == "" {
		return false
	}
	cmd := exec.Command(pyenvBin, "versions", "--bare")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		v := strings.TrimSpace(line)
		if strings.HasPrefix(v, version) {
			return true
		}
	}
	return false
}

// PyenvInstallVersion installs a Python version via pyenv. Returns error if
// installation fails. This can take several minutes for compilation.
func PyenvInstallVersion(version string) error {
	pyenvBin := pyenvBinPath()
	if pyenvBin == "" {
		return fmt.Errorf("pyenv not found")
	}

	// Find the latest patch release for this minor version
	fullVersion, err := pyenvLatestPatch(pyenvBin, version)
	if err != nil {
		fullVersion = version // fallback to exact version
	}

	cmd := exec.Command(pyenvBin, "install", "--skip-existing", fullVersion)
	cmd.Stdout = os.Stderr // show progress
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pyenv install %s: %w", fullVersion, err)
	}
	return nil
}

// PyenvEnv returns environment variables that configure a specific Python
// version via pyenv. These should be prepended to cmd.Env for setup/validation
// commands. Returns nil if pyenv or the version isn't available.
func PyenvEnv(pythonVersion string) []string {
	if !PyenvAvailable() || !PyenvVersionInstalled(pythonVersion) {
		return nil
	}

	pyenvRoot := os.Getenv("PYENV_ROOT")
	if pyenvRoot == "" {
		pyenvRoot = filepath.Join(os.Getenv("HOME"), ".pyenv")
	}

	// Find the actual installed version directory
	fullVersion := findInstalledVersion(pyenvRoot, pythonVersion)
	if fullVersion == "" {
		return nil
	}

	venvBin := filepath.Join(pyenvRoot, "versions", fullVersion, "bin")

	// Prepend pyenv's Python to PATH so all commands use the right version
	currentPath := os.Getenv("PATH")
	return []string{
		fmt.Sprintf("PATH=%s:%s", venvBin, currentPath),
		fmt.Sprintf("PYENV_VERSION=%s", fullVersion),
		fmt.Sprintf("PYENV_ROOT=%s", pyenvRoot),
	}
}

// PyenvPythonPath returns the path to a specific Python binary managed by pyenv.
// Returns empty string if not available.
func PyenvPythonPath(pythonVersion string) string {
	pyenvRoot := os.Getenv("PYENV_ROOT")
	if pyenvRoot == "" {
		pyenvRoot = filepath.Join(os.Getenv("HOME"), ".pyenv")
	}
	fullVersion := findInstalledVersion(pyenvRoot, pythonVersion)
	if fullVersion == "" {
		return ""
	}
	return filepath.Join(pyenvRoot, "versions", fullVersion, "bin", "python3")
}

func pyenvBinPath() string {
	pyenvRoot := os.Getenv("PYENV_ROOT")
	if pyenvRoot == "" {
		pyenvRoot = filepath.Join(os.Getenv("HOME"), ".pyenv")
	}
	bin := filepath.Join(pyenvRoot, "bin", "pyenv")
	if _, err := os.Stat(bin); err != nil {
		return ""
	}
	return bin
}

// pyenvLatestPatch finds the latest patch version for a given minor version.
// e.g., "3.9" → "3.9.21"
func pyenvLatestPatch(pyenvBin, minorVersion string) (string, error) {
	cmd := exec.Command(pyenvBin, "install", "--list")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}

	var latest string
	for _, line := range strings.Split(string(out), "\n") {
		v := strings.TrimSpace(line)
		if strings.HasPrefix(v, minorVersion+".") && !strings.Contains(v, "-") {
			latest = v // keep the last (highest) matching version
		}
	}
	if latest == "" {
		return "", fmt.Errorf("no patch version found for %s", minorVersion)
	}
	return latest, nil
}

// findInstalledVersion finds the full version string for an installed pyenv version.
// e.g., "3.9" might match "3.9.21"
func findInstalledVersion(pyenvRoot, minorVersion string) string {
	versionsDir := filepath.Join(pyenvRoot, "versions")
	entries, err := os.ReadDir(versionsDir)
	if err != nil {
		return ""
	}

	var best string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, minorVersion+".") || name == minorVersion {
			best = name // take the last (alphabetically highest) match
		}
	}
	return best
}
