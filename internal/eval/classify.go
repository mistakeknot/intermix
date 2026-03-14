package eval

import (
	"strings"
	"time"
)

// ClassifyFromRunDetails applies the fixed taxonomy based on observable signals.
// Classification order (most specific first):
//  1. timeout:       exit -1 + stderr contains "deadline exceeded"/"timeout"/"signal: killed"
//  2. crash:         exit != 0 + stderr contains "segfault"/"panic"/"SIGSEGV"/"fatal error"
//  3. context_limit: stderr contains "context limit"/"token limit"/"max_tokens"/"context window"
//  4. tool_failure:  exit != 0 + stderr contains "tool"/"MCP"/"failed to call"
//  5. no_progress:   files_changed == 0 && !validation_passed
//  6. success:       files_changed > 0 && validation_passed
//  7. partial:       files_changed > 0 && !validation_passed
//  8. default:       tool_failure
func ClassifyFromRunDetails(rd *RunDetails, repo, task string) CellResult {
	cr := CellResult{
		Type:                "cell_result",
		Repo:                repo,
		Task:                task,
		ValidationPassed:    rd.ValidationPassed,
		DurationMs:          rd.DurationMs,
		ExitCode:            rd.ExitCode,
		FilesChanged:        rd.FilesChanged,
		InputTokens:         rd.InputTokens,
		OutputTokens:        rd.OutputTokens,
		CacheCreationTokens: rd.CacheCreationTokens,
		CacheReadTokens:     rd.CacheReadTokens,
		TokensUsed:          rd.InputTokens + rd.OutputTokens,
		Timestamp:           time.Now().UTC().Format(time.RFC3339),
	}

	stderr := rd.Stderr

	// 1. Timeout
	if rd.ExitCode == -1 && containsAny(stderr, "deadline exceeded", "timeout", "signal: killed") {
		cr.Outcome = OutcomeTimeout
		cr.Severity = SeverityCritical
		cr.FailureReason = "process timed out or was killed"
		return cr
	}

	// 2. Crash
	if rd.ExitCode != 0 && containsAny(stderr, "segfault", "panic", "SIGSEGV", "fatal error") {
		cr.Outcome = OutcomeCrash
		cr.Severity = SeverityCritical
		cr.FailureReason = "process crashed"
		return cr
	}

	// 3. Context limit
	if containsAny(stderr, "context limit", "token limit", "max_tokens", "context window") {
		cr.Outcome = OutcomeContextLimit
		cr.Severity = SeverityCritical
		cr.FailureReason = "hit context or token limit"
		return cr
	}

	// 4. Tool failure
	if rd.ExitCode != 0 && containsAny(stderr, "tool", "MCP", "failed to call") {
		cr.Outcome = OutcomeToolFailure
		cr.Severity = SeverityDegraded
		cr.FailureReason = "tool or MCP call failed"
		return cr
	}

	// 5. No progress
	if rd.FilesChanged == 0 && !rd.ValidationPassed {
		cr.Outcome = OutcomeNoProgress
		cr.Severity = SeverityCritical
		cr.FailureReason = "no files changed and validation failed"
		return cr
	}

	// 6. Success
	if rd.FilesChanged > 0 && rd.ValidationPassed {
		cr.Outcome = OutcomeSuccess
		cr.Severity = SeverityAcceptable
		return cr
	}

	// 7. Partial
	if rd.FilesChanged > 0 && !rd.ValidationPassed {
		cr.Outcome = OutcomePartial
		cr.Severity = SeverityDegraded
		cr.FailureReason = "files changed but validation failed"
		return cr
	}

	// 8. Default fallback
	cr.Outcome = OutcomeToolFailure
	cr.Severity = SeverityDegraded
	cr.FailureReason = "unclassified failure"
	return cr
}

// containsAny returns true if s contains any of the given substrings (case-insensitive).
func containsAny(s string, substrs ...string) bool {
	lower := strings.ToLower(s)
	for _, sub := range substrs {
		if strings.Contains(lower, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}
