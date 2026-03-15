package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mistakeknot/intermix/internal/eval"
)

func main() {
	manifestPath := flag.String("manifest", "", "Path to intermix YAML manifest")
	datasetPath := flag.String("dataset", "", "Path to SWE-bench JSONL (generates manifest from dataset)")
	instanceIDs := flag.String("instances", "", "Comma-separated instance IDs to filter (only with -dataset)")
	outDir := flag.String("out", ".", "Output directory for results")
	beadID := flag.String("bead", "", "Parent bead ID")
	useDocker := flag.Bool("docker", false, "Run cells in Docker containers with version-specific Python")
	flag.Parse()

	if *manifestPath == "" && *datasetPath == "" {
		fmt.Fprintln(os.Stderr, "usage: swebench-pilot -manifest <path> | -dataset <path> [-instances <ids>]")
		os.Exit(1)
	}

	var manifest *eval.Manifest

	if *datasetPath != "" {
		// Load from SWE-bench JSONL
		var ids []string
		if *instanceIDs != "" {
			ids = strings.Split(*instanceIDs, ",")
		}
		instances, err := eval.LoadSWEBenchDataset(*datasetPath, ids)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load dataset: %v\n", err)
			os.Exit(1)
		}
		manifest = eval.SWEBenchToManifest(instances)
		fmt.Printf("Loaded %d instances from dataset\n", len(instances))
	} else {
		var err error
		manifest, err = eval.ParseManifest(*manifestPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "parse manifest: %v\n", err)
			os.Exit(1)
		}
	}

	cells := eval.ExpandMatrix(manifest)
	fmt.Printf("Campaign: %d repos × %d tasks = %d cells\n", len(manifest.Repos), len(manifest.Tasks), len(cells))
	fmt.Printf("Bead: %s\n", *beadID)
	fmt.Printf("Output: %s\n\n", *outDir)

	os.MkdirAll(*outDir, 0755)

	// Write manifest cache
	manifestCache, _ := json.Marshal(manifest)
	os.WriteFile(filepath.Join(*outDir, ".intermix-manifest.json"), manifestCache, 0644)

	// Initialize JSONL state
	cfg := eval.MatrixConfig{
		Name:                   "swebench-lite-pilot",
		ManifestPath:           *manifestPath,
		TotalCells:             len(cells),
		MaxCells:               len(cells),
		MaxConsecutiveFailures: len(cells),
		Timeout:                "600s",
		BeadID:                 *beadID,
	}
	for _, r := range manifest.Repos {
		cfg.RepoIDs = append(cfg.RepoIDs, r.ID)
	}
	for _, t := range manifest.Tasks {
		cfg.TaskIDs = append(cfg.TaskIDs, t.ID)
	}
	jsonlPath := filepath.Join(*outDir, "intermix.jsonl")
	if err := eval.WriteConfig(jsonlPath, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "write config: %v\n", err)
		os.Exit(1)
	}

	// Run each cell
	var results []eval.CellResult
	for i, cell := range cells {
		repo := findRepo(manifest, cell.RepoID)
		task := findTask(manifest, cell.TaskID)
		if repo == nil || task == nil {
			fmt.Printf("[%d/%d] SKIP %s×%s — not found in manifest\n", i+1, len(cells), cell.RepoID, cell.TaskID)
			continue
		}

		fmt.Printf("\n═══════════════════════════════════════════════════\n")
		fmt.Printf("[%d/%d] %s × %s\n", i+1, len(cells), cell.RepoID, cell.TaskID)
		fmt.Printf("═══════════════════════════════════════════════════\n")

		// Docker mode: run entire pipeline inside a container
		if *useDocker {
			// Determine Python version from task metadata
			version := task.Metadata["version"]
			repoName := task.Metadata["repo"]
			if repoName == "" {
				// Infer repo name from repo ID (django__django → django/django)
				repoName = strings.ReplaceAll(repo.ID, "__", "/")
			}
			if version == "" {
				version = task.Metadata["version"]
			}
			pyVer := eval.LookupPythonVersion(repoName, version)
			image := eval.DockerImageTag(pyVer)

			fmt.Printf("Docker: %s (Python %s)\n", image, pyVer)

			if !eval.DockerImageExists(image) {
				fmt.Printf("  IMAGE NOT FOUND — skipping (build with: ./docker/build-images.sh %s)\n", pyVer)
				cr := eval.ClassifyFromRunDetails(&eval.RunDetails{
					Repo: cell.RepoID, Task: cell.TaskID,
					ExitCode: -1, Stderr: fmt.Sprintf("Docker image %s not found", image),
				}, cell.RepoID, cell.TaskID)
				cr.Outcome = eval.OutcomeSetupFailure
				cr.Severity = eval.SeverityCritical
				results = append(results, cr)
				eval.WriteCellResult(jsonlPath, cr)
				continue
			}

			cellTimeout := 10 * time.Minute
			if manifest.Defaults.Timeout != "" {
				if parsed, err := time.ParseDuration(manifest.Defaults.Timeout); err == nil {
					cellTimeout = parsed
				}
			}

			dockerCfg := eval.DockerConfig{
				Image:     image,
				PythonVer: pyVer,
			}
			rd := eval.RunCellDocker(context.Background(), repo, task, dockerCfg, cellTimeout)
			fmt.Printf("  Result: exit=%d, duration=%ds, files_changed=%d, validation=%v\n",
				rd.ExitCode, rd.DurationMs/1000, rd.FilesChanged, rd.ValidationPassed)
			if rd.Stderr != "" && rd.ExitCode != 0 {
				// Show last 5 lines of stderr for debugging
				lines := strings.Split(rd.Stderr, "\n")
				start := len(lines) - 5
				if start < 0 {
					start = 0
				}
				for _, l := range lines[start:] {
					if l != "" {
						fmt.Printf("  stderr: %s\n", l)
					}
				}
			}

			cr := eval.ClassifyFromRunDetails(rd, cell.RepoID, cell.TaskID)
			cr.LLMAnalysis = fmt.Sprintf("Docker pilot (py%s): exit=%d, files=%d, validation=%v", pyVer, rd.ExitCode, rd.FilesChanged, rd.ValidationPassed)
			results = append(results, cr)
			eval.WriteCellResult(jsonlPath, cr)
			eval.EmitCellEvent(cr, *beadID)
			fmt.Printf("  Outcome: %s (%s)\n", cr.Outcome, cr.Severity)

			// Clean up
			continue
		}

		cellID := fmt.Sprintf("%s-%s-%d", cell.RepoID, cell.TaskID, time.Now().Unix())
		cloneDir := filepath.Join(os.TempDir(), "intermix", cellID)

		// 0. Pyenv: determine if we need a specific Python version
		var pyenvEnvVars []string
		if repoName := task.Metadata["repo"]; repoName != "" {
			version := task.Metadata["version"]
			pyVer := eval.LookupPythonVersion(repoName, version)
			hostPy := "3.12" // current system Python
			if pyVer != hostPy {
				if eval.PyenvAvailable() && eval.PyenvVersionInstalled(pyVer) {
					pyenvEnvVars = eval.PyenvEnv(pyVer)
					fmt.Printf("pyenv: Python %s (via pyenv)\n", pyVer)
				} else if eval.PyenvAvailable() {
					fmt.Printf("pyenv: Python %s needed but not installed. Installing...\n", pyVer)
					if err := eval.PyenvInstallVersion(pyVer); err != nil {
						fmt.Printf("  PYENV INSTALL FAILED: %v\n", err)
						fmt.Printf("  Falling back to system Python %s\n", hostPy)
					} else {
						pyenvEnvVars = eval.PyenvEnv(pyVer)
						fmt.Printf("  Installed Python %s\n", pyVer)
					}
				} else {
					fmt.Printf("pyenv: Python %s needed but pyenv not available (using system %s)\n", pyVer, hostPy)
				}
			}
		}

		// 1. Clone at base_commit
		commit := task.Metadata["base_commit"]
		if commit != "" {
			fmt.Printf("Cloning %s at %s...\n", repo.URL, commit[:12])
		} else {
			fmt.Printf("Cloning %s...\n", repo.URL)
		}
		start := time.Now()
		if err := eval.CloneRepoAt(repo.URL, cloneDir, commit); err != nil {
			fmt.Printf("  CLONE FAILED: %v\n", err)
			cr := eval.ClassifyFromRunDetails(&eval.RunDetails{
				CellID: cellID, Repo: cell.RepoID, Task: cell.TaskID,
				ExitCode: -1, Stderr: err.Error(),
			}, cell.RepoID, cell.TaskID)
			cr.Outcome = eval.OutcomeSetupFailure
			cr.Severity = eval.SeverityCritical
			results = append(results, cr)
			eval.WriteCellResult(jsonlPath, cr)
			continue
		}
		cloneDur := time.Since(start)
		fmt.Printf("  Cloned in %v\n", cloneDur.Round(time.Second))

		// 2. Setup (with pyenv env if needed)
		if repo.Setup != "" {
			fmt.Printf("Running setup...\n")
			if err := eval.RunSetupWithEnv(cloneDir, repo.Setup, pyenvEnvVars); err != nil {
				fmt.Printf("  SETUP FAILED: %v\n", err)
				cr := eval.ClassifyFromRunDetails(&eval.RunDetails{
					CellID: cellID, Repo: cell.RepoID, Task: cell.TaskID,
					ExitCode: -1, Stderr: err.Error(), CloneDir: cloneDir,
				}, cell.RepoID, cell.TaskID)
				cr.Outcome = eval.OutcomeSetupFailure
				cr.Severity = eval.SeverityCritical
				results = append(results, cr)
				eval.WriteCellResult(jsonlPath, cr)
				continue
			}
		}

		// 3. Spawn Skaffen
		timeout := manifest.Defaults.Timeout
		if repo.SkaffenConfig.Timeout != "" {
			timeout = repo.SkaffenConfig.Timeout
		}
		fmt.Printf("Spawning Skaffen (timeout: %s)...\n", timeout)
		rd := eval.SpawnSkaffen(cloneDir, task.Prompt, timeout)
		rd.CellID = cellID
		rd.Repo = cell.RepoID
		rd.Task = cell.TaskID
		eval.ParseSkaffenTokens(rd, rd.Stdout+rd.Stderr)
		fmt.Printf("  Skaffen: exit=%d, duration=%ds, files_changed=%d, tokens=%d/%d\n",
			rd.ExitCode, rd.DurationMs/1000, rd.FilesChanged, rd.InputTokens, rd.OutputTokens)

		// 3b. Extract patch
		patch, _ := eval.ExtractPatch(cloneDir)
		rd.Patch = patch
		if patch != "" {
			fmt.Printf("  Patch: %d bytes\n", len(patch))
		}

		// 3c. Apply test_patch
		if testPatch := task.Metadata["test_patch"]; testPatch != "" {
			fmt.Printf("Applying test_patch (%d bytes)...\n", len(testPatch))
			if err := eval.ApplyTestPatch(cloneDir, testPatch); err != nil {
				fmt.Printf("  WARNING: test_patch failed: %v\n", err)
			}
		}

		// 4. Validate
		valCmd := task.ValidationCmd
		if valCmd == "" {
			valCmd = eval.InferValidationCmd(cloneDir, repo.Language)
		}
		if valCmd != "" {
			fmt.Printf("Validating: %s\n", valCmd)
			vr := eval.RunValidationWithEnv(cloneDir, valCmd, pyenvEnvVars)
			rd.ValidationPassed = vr.Passed
			rd.ValidationOutput = vr.Output
			fmt.Printf("  Validation: passed=%v, exit=%d\n", vr.Passed, vr.ExitCode)
			if !vr.Passed {
				// Show last few lines of output
				lines := strings.Split(vr.Output, "\n")
				start := len(lines) - 10
				if start < 0 {
					start = 0
				}
				fmt.Printf("  Output (last 10 lines):\n")
				for _, l := range lines[start:] {
					fmt.Printf("    %s\n", l)
				}
			}
		}

		// 5. Classify
		cr := eval.ClassifyFromRunDetails(rd, cell.RepoID, cell.TaskID)
		cr.LLMAnalysis = fmt.Sprintf("Pilot run: exit=%d, files=%d, validation=%v", rd.ExitCode, rd.FilesChanged, rd.ValidationPassed)
		results = append(results, cr)
		eval.WriteCellResult(jsonlPath, cr)

		// Emit event
		eval.EmitCellEvent(cr, *beadID)

		fmt.Printf("  Result: %s (%s)\n", cr.Outcome, cr.Severity)

		// Clean up clone
		os.RemoveAll(cloneDir)
	}

	// Generate report
	fmt.Printf("\n\n")
	report := eval.GenerateReport(results, nil)
	fmt.Println(eval.FormatReport(report))

	// Emit campaign event
	eval.EmitCampaignEvent(report, "swebench-lite-pilot", *beadID)

	// Write report JSON
	reportData, _ := json.MarshalIndent(report, "", "  ")
	reportPath := filepath.Join(*outDir, "pilot-report.json")
	os.WriteFile(reportPath, reportData, 0644)
	fmt.Printf("\nReport written to %s\n", reportPath)
}

func findRepo(m *eval.Manifest, id string) *eval.Repo {
	for i := range m.Repos {
		if m.Repos[i].ID == id {
			return &m.Repos[i]
		}
	}
	return nil
}

func findTask(m *eval.Manifest, id string) *eval.Task {
	for i := range m.Tasks {
		if m.Tasks[i].ID == id {
			return &m.Tasks[i]
		}
	}
	return nil
}
