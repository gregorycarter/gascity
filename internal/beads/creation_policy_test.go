package beads

import (
	"strings"
	"testing"
)

func TestCreationPolicyRequiresExactlyOneConfiguredCategory(t *testing.T) {
	policy := CreationPolicy{RequiredCategories: []string{"product", "infrastructure"}}

	tests := []struct {
		name   string
		labels []string
		want   string
	}{
		{name: "missing", labels: nil, want: "requires exactly one"},
		{name: "dual", labels: []string{"product", "infrastructure"}, want: "found 2"},
		{name: "one", labels: []string{"theme:backend", "product"}},
		{name: "duplicate same category", labels: []string{"product", "product"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := policy.Validate(Bead{Labels: tt.labels})
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestCreationPolicyValidateGraphPlanBeforePersistence(t *testing.T) {
	policy := CreationPolicy{RequiredCategories: []string{"product", "infrastructure"}}
	plan := &GraphApplyPlan{Nodes: []GraphApplyNode{
		{Key: "root", Title: "root", Labels: []string{"product"}},
		{Key: "step", Title: "step"},
	}}
	if err := policy.ValidateGraphPlan(plan); err == nil || !strings.Contains(err.Error(), `graph node "step"`) {
		t.Fatalf("ValidateGraphPlan() error = %v, want failing node", err)
	}
}

func TestCreationPolicyDisabledByDefault(t *testing.T) {
	if err := (CreationPolicy{}).Validate(Bead{Title: "ordinary bead"}); err != nil {
		t.Fatalf("disabled policy rejected bead: %v", err)
	}
}

func TestCreationPolicyRejectsInvalidConfiguration(t *testing.T) {
	for _, categories := range [][]string{{"product", "product"}, {"product", " "}} {
		if err := (CreationPolicy{RequiredCategories: categories}).Validate(Bead{Labels: []string{"product"}}); err == nil {
			t.Fatalf("Validate(%q) = nil, want invalid-policy error", categories)
		}
	}
}
