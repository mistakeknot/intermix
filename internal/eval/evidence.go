package eval

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// HarvestEvidence copies Skaffen's evidence JSONL from ~/.skaffen/evidence/<sessionID>.jsonl
// to <campaignDir>/evidence/<cellID>.jsonl. Returns the destination path.
func HarvestEvidence(campaignDir, cellID, sessionID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home dir: %w", err)
	}

	srcPath := filepath.Join(home, ".skaffen", "evidence", sessionID+".jsonl")
	if _, err := os.Stat(srcPath); err != nil {
		return "", fmt.Errorf("evidence file not found: %s: %w", srcPath, err)
	}

	destDir := filepath.Join(campaignDir, "evidence")
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", fmt.Errorf("create evidence dir: %w", err)
	}

	destPath := filepath.Join(destDir, cellID+".jsonl")
	if err := copyFile(srcPath, destPath); err != nil {
		return "", fmt.Errorf("copy evidence: %w", err)
	}

	return destPath, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
