package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
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
	parallel := flag.Int("parallel", 0, "Max parallel cells (0 = auto: 2 for ≤10 cells, min(4, GOMAXPROCS/2) for larger)")
	flag.Parse()

	if *manifestPath == "" && *datasetPath == "" {
		fmt.Fprintln(os.Stderr, "usage: swebench-pilot -manifest <path> | -dataset <path> [-instances <ids>]")
		os.Exit(1)
	}

	var manifest *eval.Manifest

	if *datasetPath != "" {
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

	// Determine parallelism
	maxParallel := *parallel
	if maxParallel <= 0 {
		if len(cells) <= 10 {
			maxParallel = 2
		} else {
			maxParallel = min(4, max(2, runtime.GOMAXPROCS(0)/2))
		}
	}

	fmt.Printf("Campaign: %d repos × %d tasks = %d cells (parallel: %d)\n",
		len(manifest.Repos), len(manifest.Tasks), len(cells), maxParallel)
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

	// Pre-warm repo cache: fetch all unique repos before starting cells.
	// This avoids GitHub rate limits during parallel execution.
	var repoURLs []string
	for _, r := range manifest.Repos {
		repoURLs = append(repoURLs, r.URL)
	}
	eval.GetRepoCache().WarmRepos(repoURLs)

	// Docker mode is sequential (unchanged)
	if *useDocker {
		results := runSequentialDocker(manifest, cells, jsonlPath, *beadID)
		writeReport(results, *outDir, *beadID)
		return
	}

	// Parallel cell execution
	var (
		results   []eval.CellResult
		resultsMu sync.Mutex
		jsonlMu   sync.Mutex
		completed int64
	)

	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup

	for i, cell := range cells {
		repo := findRepo(manifest, cell.RepoID)
		task := findTask(manifest, cell.TaskID)
		if repo == nil || task == nil {
			fmt.Printf("[%d/%d] SKIP %s×%s — not found in manifest\n", i+1, len(cells), cell.RepoID, cell.TaskID)
			continue
		}

		wg.Add(1)
		go func(idx int, cell eval.Cell, repo *eval.Repo, task *eval.Task) {
			defer wg.Done()
			sem <- struct{}{}        // acquire slot
			defer func() { <-sem }() // release slot

			cr := runCell(idx, len(cells), manifest, repo, task)

			// Thread-safe result collection
			jsonlMu.Lock()
			eval.WriteCellResult(jsonlPath, cr)
			jsonlMu.Unlock()

			eval.EmitCellEvent(cr, *beadID)

			resultsMu.Lock()
			results = append(results, cr)
			resultsMu.Unlock()

			done := atomic.AddInt64(&completed, 1)
			fmt.Printf("  [%d/%d done] %s: %s (%s)\n", done, len(cells), task.ID, cr.Outcome, cr.Severity)
		}(i, cell, repo, task)
	}

	wg.Wait()

	writeReport(results, *outDir, *beadID)
}

// runCell executes a single SWE-bench cell: clone → setup → skaffen → test_patch → validate → classify.
func runCell(idx, total int, manifest *eval.Manifest, repo *eval.Repo, task *eval.Task) eval.CellResult {
	cellID := fmt.Sprintf("%s-%s-%d", repo.ID, task.ID, time.Now().UnixNano())
	cloneDir := filepath.Join(os.TempDir(), "intermix", cellID)
	defer os.RemoveAll(cloneDir)

	prefix := fmt.Sprintf("[%d/%d %s]", idx+1, total, task.ID)

	// 0. Python version resolution
	var pythonEnvVars []string
	if repoName := task.Metadata["repo"]; repoName != "" {
		version := task.Metadata["version"]
		pyVer := eval.LookupPythonVersion(repoName, version)
		hostPy := "3.12"
		if pyVer != hostPy {
			if eval.UvPythonAvailable() {
				pyPath, err := eval.UvPythonFind(pyVer)
				if err != nil {
					pyPath, err = eval.UvPythonInstall(pyVer)
				}
				if err == nil {
					pythonEnvVars = eval.UvPythonEnv(pyVer, pyPath)
				}
			}
			if len(pythonEnvVars) == 0 && eval.PyenvAvailable() && eval.PyenvVersionInstalled(pyVer) {
				pythonEnvVars = eval.PyenvEnv(pyVer)
			}
		}
	}

	// 1. Clone (from local cache — instant)
	commit := task.Metadata["base_commit"]
	start := time.Now()
	if err := eval.CloneRepoAt(repo.URL, cloneDir, commit); err != nil {
		fmt.Printf("%s CLONE FAILED: %v\n", prefix, err)
		cr := eval.ClassifyFromRunDetails(&eval.RunDetails{
			CellID: cellID, Repo: repo.ID, Task: task.ID,
			ExitCode: -1, Stderr: err.Error(),
		}, repo.ID, task.ID)
		cr.Outcome = eval.OutcomeSetupFailure
		cr.Severity = eval.SeverityCritical
		return cr
	}
	cloneDur := time.Since(start)
	fmt.Printf("%s cloned in %v\n", prefix, cloneDur.Round(time.Millisecond))

	// 2. Setup
	if repo.Setup != "" {
		if err := eval.RunSetupWithEnv(cloneDir, repo.Setup, pythonEnvVars); err != nil {
			fmt.Printf("%s SETUP FAILED: %v\n", prefix, err)
			cr := eval.ClassifyFromRunDetails(&eval.RunDetails{
				CellID: cellID, Repo: repo.ID, Task: task.ID,
				ExitCode: -1, Stderr: err.Error(), CloneDir: cloneDir,
			}, repo.ID, task.ID)
			cr.Outcome = eval.OutcomeSetupFailure
			cr.Severity = eval.SeverityCritical
			return cr
		}
	}

	// 3. Apply test_patch BEFORE spawning Skaffen so the iterate loop
	// tests against the correct SWE-bench assertions (fail_to_pass tests).
	if testPatch := task.Metadata["test_patch"]; testPatch != "" {
		if err := eval.ApplyTestPatch(cloneDir, testPatch); err != nil {
			fmt.Printf("%s WARNING: test_patch failed: %v\n", prefix, err)
		}
	}

	// 4. Spawn Skaffen with iterate-until-pass
	timeout := manifest.Defaults.Timeout
	if repo.SkaffenConfig.Timeout != "" {
		timeout = repo.SkaffenConfig.Timeout
	}
	var skaffenOpts eval.SpawnSkaffenOpts
	valCmd := task.ValidationCmd
	if valCmd == "" {
		valCmd = eval.InferValidationCmd(cloneDir, repo.Language)
	}
	if valCmd != "" {
		testCmd := valCmd
		if strings.Contains(testCmd, ".venv/bin/") {
			testCmd = "source .venv/bin/activate 2>/dev/null; " + testCmd
		}
		skaffenOpts = eval.SpawnSkaffenOpts{IterateMax: 5, TestCmd: testCmd}
	}

	fmt.Printf("%s spawning Skaffen (timeout: %s, iterate: %d)...\n", prefix, timeout, skaffenOpts.IterateMax)
	rd := eval.SpawnSkaffenWithOpts(cloneDir, task.Prompt, timeout, skaffenOpts)
	rd.CellID = cellID
	rd.Repo = repo.ID
	rd.Task = task.ID
	eval.ParseSkaffenTokens(rd, rd.Stdout+rd.Stderr)
	fmt.Printf("%s skaffen done: exit=%d, %ds, files=%d, tokens=%d/%d\n",
		prefix, rd.ExitCode, rd.DurationMs/1000, rd.FilesChanged, rd.InputTokens, rd.OutputTokens)

	// 4b. Extract patch (after Skaffen, before validation)
	patch, _ := eval.ExtractPatch(cloneDir)
	rd.Patch = patch

	// 5. Validate (same test_patch already applied, same valCmd)
	if valCmd != "" {
		vr := eval.RunValidationWithEnv(cloneDir, valCmd, pythonEnvVars)
		rd.ValidationPassed = vr.Passed
		rd.ValidationOutput = vr.Output
		fmt.Printf("%s validation: passed=%v\n", prefix, vr.Passed)
	}

	// 5. Classify
	cr := eval.ClassifyFromRunDetails(rd, repo.ID, task.ID)
	cr.LLMAnalysis = fmt.Sprintf("Pilot run: exit=%d, files=%d, validation=%v", rd.ExitCode, rd.FilesChanged, rd.ValidationPassed)
	return cr
}

// runSequentialDocker runs cells in Docker mode (unchanged, sequential).
func runSequentialDocker(manifest *eval.Manifest, cells []eval.Cell, jsonlPath, beadID string) []eval.CellResult {
	var results []eval.CellResult
	for i, cell := range cells {
		repo := findRepo(manifest, cell.RepoID)
		task := findTask(manifest, cell.TaskID)
		if repo == nil || task == nil {
			continue
		}

		fmt.Printf("\n[%d/%d] %s × %s (Docker)\n", i+1, len(cells), cell.RepoID, cell.TaskID)

		version := task.Metadata["version"]
		repoName := task.Metadata["repo"]
		if repoName == "" {
			repoName = strings.ReplaceAll(repo.ID, "__", "/")
		}
		pyVer := eval.LookupPythonVersion(repoName, version)
		image := eval.DockerImageTag(pyVer)

		if !eval.DockerImageExists(image) {
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

		dockerCfg := eval.DockerConfig{Image: image, PythonVer: pyVer}
		rd := eval.RunCellDocker(context.Background(), repo, task, dockerCfg, cellTimeout)
		cr := eval.ClassifyFromRunDetails(rd, cell.RepoID, cell.TaskID)
		cr.LLMAnalysis = fmt.Sprintf("Docker pilot (py%s): exit=%d, files=%d, validation=%v",
			pyVer, rd.ExitCode, rd.FilesChanged, rd.ValidationPassed)
		results = append(results, cr)
		eval.WriteCellResult(jsonlPath, cr)
		eval.EmitCellEvent(cr, beadID)
		fmt.Printf("  Outcome: %s (%s)\n", cr.Outcome, cr.Severity)
	}
	return results
}

func writeReport(results []eval.CellResult, outDir, beadID string) {
	fmt.Printf("\n\n")
	report := eval.GenerateReport(results, nil)
	fmt.Println(eval.FormatReport(report))

	eval.EmitCampaignEvent(report, "swebench-lite-pilot", beadID)

	reportData, _ := json.MarshalIndent(report, "", "  ")
	reportPath := filepath.Join(outDir, "pilot-report.json")
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
