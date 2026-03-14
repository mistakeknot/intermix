---
name: evaluate
description: Run a cross-repo matrix evaluation of Skaffen using intermix tools
---

# /evaluate — Cross-Repo Matrix Evaluation

Run Skaffen against unfamiliar codebases across a (repo, task) matrix. Classify outcomes, generate reports, create beads for failure patterns.

## Prerequisites

- intermix plugin installed (`claude plugin install intermix`)
- `skaffen` binary on PATH (`command -v skaffen`)
- `intermix.yaml` manifest in working directory (or pass path)

## Protocol

### Phase 1: Initialize

1. Check for existing campaign:
   - If `intermix.jsonl` exists, call `report_matrix` to show current state
   - Ask: "Resume this campaign or start fresh?"

2. Initialize new campaign:
   ```
   init_matrix(manifest_path="intermix.yaml", name="<descriptive-name>", bead_id="<parent-bead>")
   ```

3. Note the cell list and total count.

### Phase 2: Execute Matrix

Iterate through each cell sequentially. For each cell:

1. **Run the cell:**
   ```
   run_cell(repo="<repo-id>", task="<task-id>")
   ```

2. **Read the output.** Look for:
   - Exit code (0 = clean exit, non-zero = error)
   - Files changed count
   - Validation result (passed/failed)
   - Stdout/stderr content

3. **Classify the result.** Based on the output, write a brief analysis:
   ```
   classify_result(llm_analysis="<your analysis of what happened and why>")
   ```

   Classification guidelines:
   - **success**: validation passed, files changed, clean exit
   - **partial**: files changed but validation failed — describe what was attempted
   - **no_progress**: no files changed — describe what Skaffen tried to do
   - **context_limit**: look for "context" or "token" in stderr
   - **timeout**: look for "deadline" or "timeout" in stderr
   - **setup_failure**: clone or setup command failed
   - **crash**: process died with signal

4. **Log progress** every 5 cells:
   ```
   Cell 5/60: chi×add-test ✔ | cobra×refactor ✖ (context_limit) | ...
   ```

5. **Stop if circuit breaker trips** (tool returns STOPPED message).

### Phase 3: Report

After all cells complete (or circuit breaker trips):

1. Generate report:
   ```
   report_matrix(bead_id="<parent-bead>")
   ```

2. Review the report. Highlight:
   - Overall pass rate
   - Worst-performing repos (most failures)
   - Worst-performing tasks (lowest pass rate)
   - Failure clusters with ≥2 cells
   - Delta vs. previous campaign (if applicable)

3. If failure clusters exist and bead_id was provided, beads are auto-created.

### Phase 4: Archive

1. Copy results to campaign archive:
   ```bash
   mkdir -p campaigns/<campaign-name>/
   cp intermix.jsonl campaigns/<campaign-name>/results.jsonl
   ```

2. Write learnings document:
   ```bash
   # campaigns/<campaign-name>/learnings.md
   # - What worked well
   # - Failure patterns and root causes
   # - Suggested fixes for Skaffen
   # - Repos/tasks to add or remove
   ```

## Circuit Breaker

The matrix automatically stops if:
- 5 consecutive failures (something systemic is broken)
- 100 total cells (budget cap)

If tripped, the report still generates for completed cells.

## Tips

- Start with a small manifest (2 repos × 2 tasks) to verify the pipeline works
- Use `--filter=failed` on repeat runs to only re-test failures
- The LLM analysis field is your most valuable output — be specific about root causes
