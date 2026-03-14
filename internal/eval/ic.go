package eval

import (
	"encoding/json"
	"fmt"
	"os/exec"
)

// EmitCellEvent records a cell_evaluation event via ic CLI.
// Gracefully degrades if ic is not available.
func EmitCellEvent(cr CellResult, beadID string) error {
	if _, err := exec.LookPath("ic"); err != nil {
		return nil // ic not available, degrade gracefully
	}

	payload := map[string]interface{}{
		"repo":              cr.Repo,
		"task":              cr.Task,
		"outcome":           cr.Outcome,
		"severity":          cr.Severity,
		"validation_passed": cr.ValidationPassed,
		"duration_ms":       cr.DurationMs,
		"files_changed":     cr.FilesChanged,
		"tokens_used":       cr.TokensUsed,
	}

	payloadJSON, _ := json.Marshal(payload)

	args := []string{"events", "record",
		"--source=intermix",
		"--type=cell_evaluation",
		fmt.Sprintf("--payload=%s", string(payloadJSON)),
	}
	if beadID != "" {
		args = append(args, fmt.Sprintf("--bead=%s", beadID))
	}

	cmd := exec.Command("ic", args...)
	_ = cmd.Run() // best-effort
	return nil
}

// EmitCampaignEvent records campaign-level summary events.
func EmitCampaignEvent(report *Report, campaignName, beadID string) error {
	if _, err := exec.LookPath("ic"); err != nil {
		return nil
	}

	payload := map[string]interface{}{
		"campaign":      campaignName,
		"total_cells":   report.TotalCells,
		"pass_rate":     report.PassRate,
		"success_count": report.SuccessCount,
		"failure_count": report.FailureCount,
		"clusters":      len(report.FailureClusters),
	}
	if report.Delta != nil {
		payload["fixed"] = report.Delta.Fixed
		payload["regressed"] = report.Delta.Regressed
	}

	payloadJSON, _ := json.Marshal(payload)

	args := []string{"events", "record",
		"--source=intermix",
		"--type=campaign_summary",
		fmt.Sprintf("--payload=%s", string(payloadJSON)),
	}
	if beadID != "" {
		args = append(args, fmt.Sprintf("--bead=%s", beadID))
	}

	cmd := exec.Command("ic", args...)
	_ = cmd.Run()
	return nil
}
