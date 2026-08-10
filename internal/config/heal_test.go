package config

import (
	"strings"
	"testing"
	"time"
)

func TestHealConfigDefaults(t *testing.T) {
	var h HealConfig
	if h.Enabled {
		t.Fatal("heal must be opt-in: zero value Enabled = true")
	}
	if got := h.IntervalOrDefault(); got != 5*time.Minute {
		t.Errorf("IntervalOrDefault = %v, want 5m", got)
	}
	if got := h.StallAfterOrDefault(); got != 30*time.Minute {
		t.Errorf("StallAfterOrDefault = %v, want 30m", got)
	}
	if got := h.OrphanStaleAfterOrDefault(); got != 20*time.Minute {
		t.Errorf("OrphanStaleAfterOrDefault = %v, want 20m", got)
	}
	if got := h.InversionAfterOrDefault(); got != 15*time.Minute {
		t.Errorf("InversionAfterOrDefault = %v, want 15m", got)
	}
	if got := h.StuckAfterOrDefault(); got != 2*time.Hour {
		t.Errorf("StuckAfterOrDefault = %v, want 2h", got)
	}
	if got := h.ActionCooldownOrDefault(); got != time.Hour {
		t.Errorf("ActionCooldownOrDefault = %v, want 1h", got)
	}
	if got := h.MaxActionsPerPassOrDefault(); got != 5 {
		t.Errorf("MaxActionsPerPassOrDefault = %d, want 5", got)
	}
	if h.InversionPriority != 0 {
		t.Errorf("zero-value InversionPriority = %d, want 0 (P0)", h.InversionPriority)
	}
}

func TestHealConfigExplicitValuesWin(t *testing.T) {
	h := HealConfig{
		Interval:          "90s",
		StallAfter:        "1h",
		OrphanStaleAfter:  "5m",
		InversionAfter:    "2m",
		StuckAfter:        "45m",
		ActionCooldown:    "10m",
		MaxActionsPerPass: 2,
	}
	if got := h.IntervalOrDefault(); got != 90*time.Second {
		t.Errorf("IntervalOrDefault = %v, want 90s", got)
	}
	if got := h.StallAfterOrDefault(); got != time.Hour {
		t.Errorf("StallAfterOrDefault = %v, want 1h", got)
	}
	if got := h.OrphanStaleAfterOrDefault(); got != 5*time.Minute {
		t.Errorf("OrphanStaleAfterOrDefault = %v, want 5m", got)
	}
	if got := h.InversionAfterOrDefault(); got != 2*time.Minute {
		t.Errorf("InversionAfterOrDefault = %v, want 2m", got)
	}
	if got := h.StuckAfterOrDefault(); got != 45*time.Minute {
		t.Errorf("StuckAfterOrDefault = %v, want 45m", got)
	}
	if got := h.ActionCooldownOrDefault(); got != 10*time.Minute {
		t.Errorf("ActionCooldownOrDefault = %v, want 10m", got)
	}
	if got := h.MaxActionsPerPassOrDefault(); got != 2 {
		t.Errorf("MaxActionsPerPassOrDefault = %d, want 2", got)
	}
}

func TestHealConfigParsesFromTOML(t *testing.T) {
	cfg, err := Parse([]byte(`
[workspace]
name = "test"

[heal]
enabled = true
interval = "2m"
stall_after = "20m"
critical_sessions = ["coordinator"]

[[heal.target]]
rig = "demo"
queue_addresses = ["demo/merge-queue"]
main_red_check = "exit 0"
main_red_route = "demo/worker"
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	h := cfg.Heal
	if !h.Enabled {
		t.Error("Enabled = false, want true")
	}
	if h.Interval != "2m" || h.StallAfter != "20m" {
		t.Errorf("durations = (%q, %q), want (2m, 20m)", h.Interval, h.StallAfter)
	}
	if len(h.CriticalSessions) != 1 || h.CriticalSessions[0] != "coordinator" {
		t.Errorf("CriticalSessions = %v", h.CriticalSessions)
	}
	if len(h.Targets) != 1 {
		t.Fatalf("Targets = %v, want 1 entry", h.Targets)
	}
	target := h.Targets[0]
	if target.Rig != "demo" || target.MainRedRoute != "demo/worker" || target.MainRedCheck != "exit 0" {
		t.Errorf("target = %+v", target)
	}
	if len(target.QueueAddresses) != 1 || target.QueueAddresses[0] != "demo/merge-queue" {
		t.Errorf("QueueAddresses = %v", target.QueueAddresses)
	}
}

func TestValidateHealConfig(t *testing.T) {
	base := func() *City {
		return &City{
			Rigs: []Rig{{Name: "demo"}},
		}
	}

	t.Run("nil config passes", func(t *testing.T) {
		if err := ValidateHealConfig(nil); err != nil {
			t.Fatalf("ValidateHealConfig(nil) = %v", err)
		}
	})

	t.Run("valid target passes", func(t *testing.T) {
		cfg := base()
		cfg.Heal = HealConfig{Targets: []HealTarget{{Rig: "demo"}}}
		if err := ValidateHealConfig(cfg); err != nil {
			t.Fatalf("ValidateHealConfig = %v", err)
		}
	})

	t.Run("unknown rig fails", func(t *testing.T) {
		cfg := base()
		cfg.Heal = HealConfig{Targets: []HealTarget{{Rig: "nope"}}}
		err := ValidateHealConfig(cfg)
		if err == nil || !strings.Contains(err.Error(), "not declared") {
			t.Fatalf("ValidateHealConfig = %v, want undeclared-rig error", err)
		}
	})

	t.Run("duplicate rig fails", func(t *testing.T) {
		cfg := base()
		cfg.Heal = HealConfig{Targets: []HealTarget{{Rig: "demo"}, {Rig: "demo"}}}
		err := ValidateHealConfig(cfg)
		if err == nil || !strings.Contains(err.Error(), "already targeted") {
			t.Fatalf("ValidateHealConfig = %v, want duplicate-rig error", err)
		}
	})

	t.Run("empty critical session fails", func(t *testing.T) {
		cfg := base()
		cfg.Heal = HealConfig{CriticalSessions: []string{" "}}
		err := ValidateHealConfig(cfg)
		if err == nil || !strings.Contains(err.Error(), "critical_sessions") {
			t.Fatalf("ValidateHealConfig = %v, want critical_sessions error", err)
		}
	})

	t.Run("empty queue address fails", func(t *testing.T) {
		cfg := base()
		cfg.Heal = HealConfig{Targets: []HealTarget{{Rig: "demo", QueueAddresses: []string{""}}}}
		err := ValidateHealConfig(cfg)
		if err == nil || !strings.Contains(err.Error(), "queue_addresses") {
			t.Fatalf("ValidateHealConfig = %v, want queue_addresses error", err)
		}
	})

	t.Run("negative inversion priority fails", func(t *testing.T) {
		cfg := base()
		cfg.Heal = HealConfig{InversionPriority: -1}
		err := ValidateHealConfig(cfg)
		if err == nil || !strings.Contains(err.Error(), "inversion_priority") {
			t.Fatalf("ValidateHealConfig = %v, want inversion_priority error", err)
		}
	})

	t.Run("workflow without route fails", func(t *testing.T) {
		cfg := base()
		cfg.Heal = HealConfig{Targets: []HealTarget{{Rig: "demo", MainRedWorkflow: "mol-x"}}}
		err := ValidateHealConfig(cfg)
		if err == nil || !strings.Contains(err.Error(), "main_red_workflow") {
			t.Fatalf("ValidateHealConfig = %v, want main_red_workflow error", err)
		}
	})
}
