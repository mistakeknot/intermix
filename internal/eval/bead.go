package eval

import (
	"fmt"
	"os/exec"
	"strings"
)

// BuildDebugBeadTitle formats the title for a per-cell debug bead.
func BuildDebugBeadTitle(repo, task, outcome string) string {
	return fmt.Sprintf("Stress test failure: %s/%s — %s", repo, task, outcome)
}

// BuildDebugBeadDescription formats the description with failure context.
func BuildDebugBeadDescription(cr CellResult, evidenceExcerpt, paneCapture string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Cell: %s/%s\n\n", cr.Repo, cr.Task)
	fmt.Fprintf(&b, "- **Outcome:** %s\n", cr.Outcome)
	fmt.Fprintf(&b, "- **Severity:** %s\n", cr.Severity)
	fmt.Fprintf(&b, "- **Duration:** %dms\n", cr.DurationMs)
	fmt.Fprintf(&b, "- **Exit code:** %d\n", cr.ExitCode)
	fmt.Fprintf(&b, "- **Files changed:** %d\n", cr.FilesChanged)
	fmt.Fprintf(&b, "- **Validation passed:** %v\n", cr.ValidationPassed)

	if cr.FailureReason != "" {
		fmt.Fprintf(&b, "\n## Failure Reason\n%s\n", cr.FailureReason)
	}
	if cr.LLMAnalysis != "" {
		fmt.Fprintf(&b, "\n## LLM Analysis\n%s\n", cr.LLMAnalysis)
	}
	if evidenceExcerpt != "" {
		fmt.Fprintf(&b, "\n## Evidence Excerpt\n```\n%s\n```\n", evidenceExcerpt)
	}
	if paneCapture != "" {
		fmt.Fprintf(&b, "\n## Pane Capture\n```\n%s\n```\n", paneCapture)
	}
	return b.String()
}

// BuildPatternBeadTitle formats the title for a failure cluster pattern bead.
func BuildPatternBeadTitle(cluster FailureCluster) string {
	return fmt.Sprintf("Pattern: %s across %d cells", cluster.Outcome, cluster.Count)
}

// CreateDebugBead creates a bead for a failed cell via the bd CLI.
// Returns the created bead ID, or empty string on failure (best-effort).
func CreateDebugBead(cr CellResult, parentBeadID, evidenceExcerpt, paneCapture string) string {
	if _, err := exec.LookPath("bd"); err != nil {
		return ""
	}

	title := BuildDebugBeadTitle(cr.Repo, cr.Task, cr.Outcome)
	desc := BuildDebugBeadDescription(cr, evidenceExcerpt, paneCapture)

	cmd := exec.Command("bd", "create",
		"--title", title,
		"--description", desc,
		"--type", "bug",
		"--priority", severityToPriority(cr.Severity),
	)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	// Parse bead ID from output: "Created issue: Sylveste-xxxx — ..."
	beadID := parseBeadIDFromOutput(string(out))
	if beadID == "" {
		return ""
	}

	// Set parent
	if parentBeadID != "" {
		exec.Command("bd", "update", beadID, "--parent="+parentBeadID).Run()
	}

	return beadID
}

// CreatePatternBeads creates beads for failure clusters with >=2 cells.
// Reparents individual debug beads under the pattern bead.
func CreatePatternBeads(clusters []FailureCluster, parentBeadID string, debugBeadMap map[string]string) {
	if _, err := exec.LookPath("bd"); err != nil {
		return
	}

	for i, cluster := range clusters {
		if cluster.Count < 2 {
			continue
		}

		title := BuildPatternBeadTitle(cluster)
		desc := fmt.Sprintf("Failure pattern: %s\nAffected cells: %s",
			cluster.Outcome, strings.Join(cluster.Cells, ", "))

		cmd := exec.Command("bd", "create",
			"--title", title,
			"--description", desc,
			"--type", "bug",
			"--priority", "1",
		)
		out, err := cmd.Output()
		if err != nil {
			continue
		}

		patternBeadID := parseBeadIDFromOutput(string(out))
		if patternBeadID == "" {
			continue
		}

		clusters[i].BeadID = patternBeadID

		// Set parent to campaign epic
		if parentBeadID != "" {
			exec.Command("bd", "update", patternBeadID, "--parent="+parentBeadID).Run()
		}

		// Reparent debug beads under pattern bead
		for _, cellKey := range cluster.Cells {
			if debugID, ok := debugBeadMap[cellKey]; ok {
				exec.Command("bd", "update", debugID, "--parent="+patternBeadID).Run()
			}
		}
	}
}

func severityToPriority(severity string) string {
	switch severity {
	case SeverityCritical:
		return "1"
	case SeverityDegraded:
		return "2"
	default:
		return "3"
	}
}

func parseBeadIDFromOutput(output string) string {
	// Output format: "Created issue: Sylveste-xxxx — ..."
	const marker = "Created issue: "
	idx := strings.Index(output, marker)
	if idx < 0 {
		return ""
	}
	rest := output[idx+len(marker):]
	// Find end of bead ID (space or " —")
	end := strings.Index(rest, " ")
	if end < 0 {
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(rest[:end])
}
