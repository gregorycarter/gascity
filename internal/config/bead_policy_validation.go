package config

import (
	"fmt"
	"sort"
	"strings"
)

// ValidateRequiredCategories validates the category-label creation policy.
// Empty means the policy is disabled; once configured, every category must be
// a distinct non-empty label so enforcement cannot silently weaken.
func ValidateRequiredCategories(categories []string) error {
	seen := make(map[string]struct{}, len(categories))
	for _, raw := range categories {
		category := strings.TrimSpace(raw)
		if category == "" {
			return fmt.Errorf("beads.required_categories contains an empty category")
		}
		if category != raw {
			return fmt.Errorf("beads.required_categories category %q has surrounding whitespace", raw)
		}
		if _, ok := seen[category]; ok {
			return fmt.Errorf("beads.required_categories contains duplicate category %q", category)
		}
		seen[category] = struct{}{}
	}
	return nil
}

var bd104CompatiblePolicyStorage = map[string][]string{
	"session":        {BeadStorageNoHistory, BeadStorageHistory},
	"wait":           {BeadStorageNoHistory, BeadStorageHistory},
	"nudge":          {BeadStorageNoHistory, BeadStorageHistory},
	"order_tracking": {BeadStorageNoHistory, BeadStorageHistory},
	"workflow":       {BeadStorageHistory},
	"wisp":           {BeadStorageHistory},
}

var bd105CompatiblePolicyStorage = map[string][]string{
	"session":        {BeadStorageNoHistory, BeadStorageHistory},
	"wait":           {BeadStorageNoHistory, BeadStorageHistory},
	"nudge":          {BeadStorageNoHistory, BeadStorageHistory},
	"order_tracking": {BeadStorageNoHistory, BeadStorageHistory},
	"workflow":       {BeadStorageNoHistory, BeadStorageHistory},
	"wisp":           {BeadStorageEphemeral, BeadStorageNoHistory, BeadStorageHistory},
}

// ValidateBeadPolicyStorageCompatibility rejects known policy/storage pairs
// that can strand or incorrectly collect Gas City beads under the configured
// bd compatibility mode.
func ValidateBeadPolicyStorageCompatibility(cfg *City, source string) error {
	if cfg == nil || len(cfg.Beads.Policies) == 0 {
		return nil
	}
	compatibility := cfg.Beads.NormalizedBDCompatibility()
	compatibleStorage := bd104CompatiblePolicyStorage
	if compatibility == BeadsBDCompatibility105 {
		compatibleStorage = bd105CompatiblePolicyStorage
	}
	names := make([]string, 0, len(cfg.Beads.Policies))
	for name := range cfg.Beads.Policies {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		policy := cfg.Beads.Policies[name]
		storage := policy.NormalizedStorage()
		if storage == "" || !ValidBeadPolicyStorage(storage) {
			continue
		}
		allowed, ok := compatibleStorage[name]
		if !ok || stringInSlice(storage, allowed) {
			continue
		}
		return fmt.Errorf("%s: [beads.policies.%s] storage = %q is not %s-compatible; allowed storage: %s",
			source, name, storage, compatibility, strings.Join(allowed, ", "))
	}
	return nil
}

func stringInSlice(value string, values []string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
