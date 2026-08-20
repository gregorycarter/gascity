package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

func requiredCategoryTestConfig() *config.City {
	return &config.City{Beads: config.BeadsConfig{RequiredCategories: []string{"product", "infrastructure"}}}
}

func TestValidateBdCreationPolicySingleCreate(t *testing.T) {
	cfg := requiredCategoryTestConfig()
	if err := validateBdCreationPolicy([]string{"create", "task", "--labels", "product,theme:api"}, cfg); err != nil {
		t.Fatalf("categorized create rejected: %v", err)
	}
	for _, args := range [][]string{
		{"create", "task"},
		{"create", "task", "--labels", "product,infrastructure"},
	} {
		if err := validateBdCreationPolicy(args, cfg); err == nil {
			t.Fatalf("validateBdCreationPolicy(%v) = nil, want refusal", args)
		}
	}
}

func TestValidateBdCreationPolicyGraphIsCheckedBeforeForwarding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, []byte(`{"nodes":[{"key":"root","title":"root","labels":["product"]},{"key":"step","title":"step"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	err := validateBdCreationPolicy([]string{"create", "--graph", path}, requiredCategoryTestConfig())
	if err == nil || !strings.Contains(err.Error(), `graph node "step"`) {
		t.Fatalf("graph validation error = %v, want failing node", err)
	}
}

func TestValidateBdCreationPolicyMarkdownChecksEveryIssue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.md")
	content := "## First\n\n### Labels\nproduct\n\n## Second\n\n### Labels\nproduct infrastructure\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	err := validateBdCreationPolicy([]string{"create", "--file", path}, requiredCategoryTestConfig())
	if err == nil || !strings.Contains(err.Error(), "markdown issue 2") {
		t.Fatalf("markdown validation error = %v, want second issue refusal", err)
	}
}

func TestValidateBdCreationPolicyRefusesUninspectableCreationPaths(t *testing.T) {
	for _, command := range []string{"batch", "import", "mol", "formula"} {
		if err := validateBdCreationPolicy([]string{command}, requiredCategoryTestConfig()); err == nil {
			t.Fatalf("%s path accepted with required categories", command)
		}
	}
}

func TestValidateBdCreationPolicyDisabled(t *testing.T) {
	if err := validateBdCreationPolicy([]string{"create", "task"}, &config.City{}); err != nil {
		t.Fatalf("disabled policy rejected create: %v", err)
	}
}
