package eval

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHarvestEvidence(t *testing.T) {
	// Set up fake Skaffen evidence directory
	home := t.TempDir()
	t.Setenv("HOME", home)
	evidenceDir := filepath.Join(home, ".skaffen", "evidence")
	os.MkdirAll(evidenceDir, 0755)

	// Write a fake evidence file
	sessionID := "test-session-123"
	evidencePath := filepath.Join(evidenceDir, sessionID+".jsonl")
	os.WriteFile(evidencePath, []byte(`{"turn":1,"phase":"act"}`+"\n"), 0644)

	// Harvest
	campaignDir := t.TempDir()
	cellID := "chi-add-test"
	destPath, err := HarvestEvidence(campaignDir, cellID, sessionID)
	if err != nil {
		t.Fatal(err)
	}

	expected := filepath.Join(campaignDir, "evidence", cellID+".jsonl")
	if destPath != expected {
		t.Errorf("unexpected dest: %s", destPath)
	}

	// Verify file was copied
	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"turn":1,"phase":"act"}`+"\n" {
		t.Errorf("unexpected content: %s", string(data))
	}
}

func TestHarvestEvidenceMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	campaignDir := t.TempDir()
	_, err := HarvestEvidence(campaignDir, "chi-add-test", "nonexistent-session")
	if err == nil {
		t.Error("expected error for missing evidence file")
	}
}
