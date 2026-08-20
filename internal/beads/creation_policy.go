package beads

import (
	"fmt"
	"sort"
	"strings"
)

// CreationPolicy describes labels that must classify every newly-created bead.
// An empty RequiredCategories slice disables the policy for backwards
// compatibility; a configured policy requires exactly one distinct category
// label and never supplies a default.
type CreationPolicy struct {
	RequiredCategories []string
}

// Validate rejects a bead that does not carry exactly one configured category.
// Labels outside RequiredCategories remain orthogonal labels and are allowed.
func (p CreationPolicy) Validate(b Bead) error {
	allowed, err := p.allowed()
	if err != nil {
		return err
	}
	if len(allowed) == 0 {
		return nil
	}

	matched := make(map[string]struct{}, len(allowed))
	for _, label := range b.Labels {
		if _, ok := allowed[label]; ok {
			matched[label] = struct{}{}
		}
	}
	if len(matched) == 1 {
		return nil
	}

	categories := make([]string, 0, len(matched))
	for category := range matched {
		categories = append(categories, category)
	}
	sort.Strings(categories)
	if len(categories) == 0 {
		return fmt.Errorf("bead creation requires exactly one category label; allowed categories: %s", strings.Join(sortedKeys(allowed), ", "))
	}
	return fmt.Errorf("bead creation requires exactly one category label; found %d (%s), allowed categories: %s", len(categories), strings.Join(categories, ", "), strings.Join(sortedKeys(allowed), ", "))
}

// ValidateGraphPlan rejects a graph before any node or edge is persisted.
func (p CreationPolicy) ValidateGraphPlan(plan *GraphApplyPlan) error {
	if plan == nil {
		return nil
	}
	for _, node := range plan.Nodes {
		if err := p.Validate(Bead{Title: node.Title, Labels: node.Labels}); err != nil {
			return fmt.Errorf("graph node %q: %w", node.Key, err)
		}
	}
	return nil
}

func (p CreationPolicy) allowed() (map[string]struct{}, error) {
	if len(p.RequiredCategories) == 0 {
		return nil, nil
	}
	allowed := make(map[string]struct{}, len(p.RequiredCategories))
	for _, raw := range p.RequiredCategories {
		category := strings.TrimSpace(raw)
		if category == "" {
			return nil, fmt.Errorf("bead creation policy contains an empty category")
		}
		if category != raw {
			return nil, fmt.Errorf("bead creation policy category %q has surrounding whitespace", raw)
		}
		if _, exists := allowed[category]; exists {
			return nil, fmt.Errorf("bead creation policy contains duplicate category %q", category)
		}
		allowed[category] = struct{}{}
	}
	return allowed, nil
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
