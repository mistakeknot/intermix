package eval

import (
	"testing"
)

func TestEmitCellEvent_GracefulDegradation(t *testing.T) {
	// Should not error even if ic is not installed
	cr := CellResult{Repo: "chi", Task: "add-test", Outcome: OutcomeSuccess}
	err := EmitCellEvent(cr, "Sylveste-ome7")
	if err != nil {
		t.Errorf("expected graceful degradation, got: %v", err)
	}
}

func TestEmitCampaignEvent_GracefulDegradation(t *testing.T) {
	report := &Report{
		TotalCells:   10,
		SuccessCount: 8,
		FailureCount: 2,
		PassRate:     80.0,
	}
	err := EmitCampaignEvent(report, "test-campaign", "Sylveste-ome7")
	if err != nil {
		t.Errorf("expected graceful degradation, got: %v", err)
	}
}
