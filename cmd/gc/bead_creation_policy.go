package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// validateBdCreationPolicy checks creation-capable bd passthroughs before the
// subprocess is started. Unsupported alternate creation grammars are refused
// while the policy is enabled rather than forwarded without validation.
func validateBdCreationPolicy(args []string, cfg *config.City) error {
	if cfg == nil || len(cfg.Beads.RequiredCategories) == 0 || len(args) == 0 {
		return nil
	}
	policy := beads.CreationPolicy{RequiredCategories: cfg.Beads.RequiredCategories}
	switch args[0] {
	case "create", "new":
		labels, graphFile, markdownFile, err := bdCreateLabels(args[1:])
		if err != nil {
			return err
		}
		if graphFile != "" {
			return validateBdGraphCategories(graphFile, policy)
		}
		if markdownFile != "" {
			return validateBdMarkdownCategories(markdownFile, policy)
		}
		return policy.Validate(beads.Bead{Labels: labels})
	case "batch", "import", "mol", "formula":
		return fmt.Errorf("bd %s is refused while beads.required_categories is enabled: this creation path cannot be validated before persistence; use `bd create` with category labels", args[0])
	default:
		return nil
	}
}

func bdCreateLabels(args []string) (labels []string, graphFile, markdownFile string, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, hasValue := strings.Cut(arg, "=")
		switch name {
		case "--labels", "--label", "-l":
			if !hasValue {
				if i+1 >= len(args) {
					return nil, "", "", fmt.Errorf("bd create: %s requires a value", name)
				}
				i++
				value = args[i]
			}
			labels = append(labels, splitBdLabels(value)...)
		case "--graph":
			if !hasValue {
				if i+1 >= len(args) {
					return nil, "", "", fmt.Errorf("bd create: --graph requires a file")
				}
				i++
				value = args[i]
			}
			graphFile = value
		case "--file", "-f":
			if !hasValue {
				if i+1 >= len(args) {
					return nil, "", "", fmt.Errorf("bd create: %s requires a file", name)
				}
				i++
				value = args[i]
			}
			markdownFile = value
		}
	}
	return labels, graphFile, markdownFile, nil
}

func splitBdLabels(value string) []string {
	var labels []string
	for _, label := range strings.Split(value, ",") {
		if label = strings.TrimSpace(label); label != "" {
			labels = append(labels, label)
		}
	}
	return labels
}

func validateBdGraphCategories(path string, policy beads.CreationPolicy) error {
	data, err := os.ReadFile(path) // #nosec G304 -- path is an explicit bd argument
	if err != nil {
		return fmt.Errorf("reading bd graph plan %q: %w", path, err)
	}
	var plan beads.GraphApplyPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		return fmt.Errorf("reading bd graph plan %q: %w", path, err)
	}
	return policy.ValidateGraphPlan(&plan)
}

func validateBdMarkdownCategories(path string, policy beads.CreationPolicy) error {
	data, err := os.ReadFile(path) // #nosec G304 -- path is an explicit bd argument
	if err != nil {
		return fmt.Errorf("reading bd markdown file %q: %w", path, err)
	}
	lines := strings.Split(string(data), "\n")
	var labels []string
	inLabels := false
	issueCount := 0
	validate := func() error {
		if issueCount == 0 {
			return nil
		}
		if err := policy.Validate(beads.Bead{Labels: labels}); err != nil {
			return fmt.Errorf("markdown issue %d: %w", issueCount, err)
		}
		labels = nil
		return nil
	}
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "## "):
			if err := validate(); err != nil {
				return err
			}
			issueCount++
			inLabels = false
		case strings.HasPrefix(line, "### "):
			inLabels = strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(line, "### ")), "labels")
		case inLabels:
			labels = append(labels, splitBdLabels(line)...)
		}
	}
	if err := validate(); err != nil {
		return err
	}
	if issueCount == 0 {
		return fmt.Errorf("bd markdown file %q contains no issues", path)
	}
	return nil
}
