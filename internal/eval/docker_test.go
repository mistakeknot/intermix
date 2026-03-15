package eval

import "testing"

func TestLookupPythonVersion(t *testing.T) {
	tests := []struct {
		repo, version, want string
	}{
		{"django/django", "3.0", "3.6"},
		{"django/django", "4.1", "3.9"},
		{"django/django", "5.0", "3.11"},
		{"scikit-learn/scikit-learn", "0.21", "3.6"},
		{"sympy/sympy", "1.5", "3.9"},
		{"unknown/repo", "1.0", "3.9"}, // default
	}
	for _, tt := range tests {
		got := LookupPythonVersion(tt.repo, tt.version)
		if got != tt.want {
			t.Errorf("LookupPythonVersion(%q, %q) = %q, want %q", tt.repo, tt.version, got, tt.want)
		}
	}
}

func TestDockerImageTag(t *testing.T) {
	if got := DockerImageTag("3.9"); got != "intermix-swebench:py3.9" {
		t.Errorf("DockerImageTag(3.9) = %q", got)
	}
}

func TestBuildDockerScript(t *testing.T) {
	repo := &Repo{ID: "test__repo", URL: "https://github.com/test/repo", Setup: "pip install -e ."}
	task := &Task{
		ID:     "test-123",
		Prompt: "Fix the bug",
		Metadata: map[string]string{
			"base_commit": "abc123def456",
			"test_patch":  "diff --git a/test.py b/test.py\n+pass\n",
		},
		ValidationCmd: "pytest -x",
	}

	script := buildDockerScript(repo, task)

	// Should contain key phases
	if !contains(script, "::PHASE::clone") {
		t.Error("missing clone phase")
	}
	if !contains(script, "::PHASE::setup") {
		t.Error("missing setup phase")
	}
	if !contains(script, "::PHASE::skaffen") {
		t.Error("missing skaffen phase")
	}
	if !contains(script, "::PHASE::validate") {
		t.Error("missing validate phase")
	}
	if !contains(script, "git checkout") {
		t.Error("missing git checkout for base_commit")
	}
	if !contains(script, "base64 -d") {
		t.Error("missing base64 decode for test_patch")
	}
	if !contains(script, "--provider anthropic") {
		t.Error("missing --provider anthropic flag")
	}
}

func TestConvertTestIDToPytest(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		// Unittest style
		{"test_fast_delete (delete.tests.FastDeleteTests)", "delete/tests::FastDeleteTests::test_fast_delete"},
		// Already pytest
		{"tests/test_foo.py::TestClass::test_method", "tests/test_foo.py::TestClass::test_method"},
		// Bare name (should return empty → use -k)
		{"test_function", ""},
	}
	for _, tt := range tests {
		got := convertTestIDToPytest(tt.input)
		if got != tt.want {
			t.Errorf("convertTestIDToPytest(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) > 0 && len(sub) > 0 && findSubstring(s, sub)
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
