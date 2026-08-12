package config

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/shellquote"
)

func TestBuildProviderLaunchCommandAddsDefaultsAndSettings(t *testing.T) {
	dir := t.TempDir()
	runtimeDir := filepath.Join(dir, ".gc")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "settings.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	spec := BuiltinProviders()["claude"]
	rp := specToResolved("claude", &spec)

	got, err := BuildProviderLaunchCommand(dir, rp, nil, "")
	if err != nil {
		t.Fatalf("BuildProviderLaunchCommand: %v", err)
	}

	wantCommand := fmt.Sprintf("claude --dangerously-skip-permissions --effort max --settings %q", filepath.Join(dir, ".gc", "settings.json"))
	if got.Command != wantCommand {
		t.Fatalf("Command = %q, want %q", got.Command, wantCommand)
	}
	if got.SettingsPath != filepath.Join(dir, ".gc", "settings.json") {
		t.Fatalf("SettingsPath = %q, want %q", got.SettingsPath, filepath.Join(dir, ".gc", "settings.json"))
	}
	if got.SettingsRel != filepath.Join(".gc", "settings.json") {
		t.Fatalf("SettingsRel = %q, want %q", got.SettingsRel, filepath.Join(".gc", "settings.json"))
	}
}

func TestBuildProviderLaunchCommandAppliesOptionOverrides(t *testing.T) {
	spec := BuiltinProviders()["claude"]
	rp := specToResolved("claude", &spec)

	got, err := BuildProviderLaunchCommand("", rp, map[string]string{
		"permission_mode": "plan",
		"effort":          "low",
	}, "")
	if err != nil {
		t.Fatalf("BuildProviderLaunchCommand: %v", err)
	}

	want := "claude --permission-mode plan --effort low"
	if got.Command != want {
		t.Fatalf("Command = %q, want %q", got.Command, want)
	}
	if got.SettingsPath != "" || got.SettingsRel != "" {
		t.Fatalf("unexpected settings source: %#v", got)
	}
}

func TestBuildProviderLaunchCommandIgnoresInitialMessageOverride(t *testing.T) {
	spec := BuiltinProviders()["claude"]
	rp := specToResolved("claude", &spec)

	got, err := BuildProviderLaunchCommand("", rp, map[string]string{
		"initial_message": "hello",
		"effort":          "low",
	}, "")
	if err != nil {
		t.Fatalf("BuildProviderLaunchCommand: %v", err)
	}

	want := "claude --dangerously-skip-permissions --effort low"
	if got.Command != want {
		t.Fatalf("Command = %q, want %q", got.Command, want)
	}
}

func TestProviderOptionMapCapacity(t *testing.T) {
	tests := []struct {
		name         string
		defaultsLen  int
		overridesLen int
		want         int
	}{
		{
			name:         "adds safe override capacity",
			defaultsLen:  2,
			overridesLen: 3,
			want:         5,
		},
		{
			name:         "keeps defaults when overrides are empty",
			defaultsLen:  4,
			overridesLen: 0,
			want:         4,
		},
		{
			name:         "uses exact boundary when addition is safe",
			defaultsLen:  math.MaxInt - 1,
			overridesLen: 1,
			want:         math.MaxInt,
		},
		{
			name:         "skips override capacity when addition would overflow",
			defaultsLen:  math.MaxInt,
			overridesLen: 1,
			want:         math.MaxInt,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := providerOptionMapCapacity(tt.defaultsLen, tt.overridesLen); got != tt.want {
				t.Fatalf("providerOptionMapCapacity(%d, %d) = %d, want %d", tt.defaultsLen, tt.overridesLen, got, tt.want)
			}
		})
	}
}

func TestBuildProviderLaunchCommandUsesACPCommand(t *testing.T) {
	rp := &ResolvedProvider{
		Command: "custom-opencode",
		ACPArgs: []string{"acp"},
	}

	t.Run("acp transport uses ACPCommandString", func(t *testing.T) {
		got, err := BuildProviderLaunchCommand("", rp, nil, "acp")
		if err != nil {
			t.Fatalf("BuildProviderLaunchCommand: %v", err)
		}
		want := "custom-opencode acp"
		if got.Command != want {
			t.Fatalf("Command = %q, want %q", got.Command, want)
		}
	})

	t.Run("default transport uses CommandString", func(t *testing.T) {
		got, err := BuildProviderLaunchCommand("", rp, nil, "")
		if err != nil {
			t.Fatalf("BuildProviderLaunchCommand: %v", err)
		}
		want := "custom-opencode"
		if got.Command != want {
			t.Fatalf("Command = %q, want %q", got.Command, want)
		}
	})

	t.Run("tmux transport uses CommandString", func(t *testing.T) {
		got, err := BuildProviderLaunchCommand("", rp, nil, "tmux")
		if err != nil {
			t.Fatalf("BuildProviderLaunchCommand: %v", err)
		}
		want := "custom-opencode"
		if got.Command != want {
			t.Fatalf("Command = %q, want %q", got.Command, want)
		}
	})

	t.Run("unknown transport errors", func(t *testing.T) {
		_, err := BuildProviderLaunchCommand("", rp, nil, "stdio")
		if err == nil {
			t.Fatal("BuildProviderLaunchCommand() error = nil, want unknown transport error")
		}
	})
}

func TestBuildProviderLaunchCommandWithoutOptionsSkipsDefaultsButKeepsSettings(t *testing.T) {
	dir := t.TempDir()
	runtimeDir := filepath.Join(dir, ".gc")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "settings.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	spec := BuiltinProviders()["claude"]
	rp := specToResolved("claude", &spec)

	got, err := BuildProviderLaunchCommandWithoutOptions(dir, rp, "")
	if err != nil {
		t.Fatalf("BuildProviderLaunchCommandWithoutOptions: %v", err)
	}

	wantCommand := fmt.Sprintf("claude --settings %q", filepath.Join(dir, ".gc", "settings.json"))
	if got.Command != wantCommand {
		t.Fatalf("Command = %q, want %q", got.Command, wantCommand)
	}
	if got.SettingsPath != filepath.Join(dir, ".gc", "settings.json") {
		t.Fatalf("SettingsPath = %q, want %q", got.SettingsPath, filepath.Join(dir, ".gc", "settings.json"))
	}
	if got.SettingsRel != filepath.Join(".gc", "settings.json") {
		t.Fatalf("SettingsRel = %q, want %q", got.SettingsRel, filepath.Join(".gc", "settings.json"))
	}
}

func TestBuildProviderLaunchCommandWithoutOptionsUsesBuiltinAncestorForSettings(t *testing.T) {
	dir := t.TempDir()
	runtimeDir := filepath.Join(dir, ".gc")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(runtimeDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	rp := &ResolvedProvider{
		Name:            "claude-max",
		BuiltinAncestor: "claude",
		Command:         "aimux",
		Args:            []string{"run", "claude", "--"},
	}

	got, err := BuildProviderLaunchCommandWithoutOptions(dir, rp, "")
	if err != nil {
		t.Fatalf("BuildProviderLaunchCommandWithoutOptions: %v", err)
	}

	if want := fmt.Sprintf("--settings %q", settingsPath); !strings.Contains(got.Command, want) {
		t.Fatalf("Command = %q, want settings arg %q", got.Command, want)
	}
	if count := strings.Count(got.Command, "--settings"); count != 1 {
		t.Fatalf("Command has %d --settings flags, want 1: %q", count, got.Command)
	}
	if got.SettingsPath != settingsPath {
		t.Fatalf("SettingsPath = %q, want %q", got.SettingsPath, settingsPath)
	}
}

func TestBuildProviderLaunchCommandWithoutOptionsIgnoresDeprecatedKindForSettings(t *testing.T) {
	dir := t.TempDir()
	runtimeDir := filepath.Join(dir, ".gc")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "settings.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	rp := &ResolvedProvider{
		Name:    "custom-provider",
		Kind:    "claude",
		Command: "custom-provider",
	}

	got, err := BuildProviderLaunchCommandWithoutOptions(dir, rp, "")
	if err != nil {
		t.Fatalf("BuildProviderLaunchCommandWithoutOptions: %v", err)
	}
	if strings.Contains(got.Command, "--settings") {
		t.Fatalf("Command = %q, want no settings from deprecated Kind fallback", got.Command)
	}
	if got.SettingsPath != "" || got.SettingsRel != "" {
		t.Fatalf("unexpected settings source from deprecated Kind fallback: %#v", got)
	}
}

// kimiCitySpec mirrors the live city's kimi provider: the builtin profile plus
// the city-side options_schema that names the provisioned K3 / K2.7 model ids
// and the thinking toggle. Every flag in that schema is a kimi *global*
// option, which the `acp` subcommand rejects.
func kimiCitySpec() ProviderSpec {
	spec := BuiltinProviders()["kimi"]
	spec.OptionsSchema = []ProviderOption{
		{
			Key:   "model",
			Label: "Model",
			Type:  "select",
			Choices: []OptionChoice{
				{Value: "k3", Label: "Kimi K3 (1M context)", FlagArgs: []string{"--model", "kimi-code/k3"}},
				{Value: "k2.7-highspeed", Label: "Kimi K2.7 Coding Highspeed", FlagArgs: []string{"--model", "kimi-code/kimi-for-coding-highspeed"}},
			},
		},
		{
			Key:   "thinking",
			Label: "Thinking",
			Type:  "select",
			Choices: []OptionChoice{
				{Value: "on", Label: "Thinking enabled", FlagArgs: []string{"--thinking"}},
				{Value: "off", Label: "Thinking disabled", FlagArgs: []string{"--no-thinking"}},
			},
		},
	}
	return spec
}

func TestBuildProviderLaunchCommandKeepsSchemaFlagsBeforeACPSubcommand(t *testing.T) {
	spec := kimiCitySpec()
	rp := specToResolved("kimi", &spec)

	tests := []struct {
		name      string
		overrides map[string]string
		want      string
	}{
		{
			// The thinking=off default inferred from the provider's own
			// --no-thinking arg is re-emitted alongside the override, so both
			// global flags have to move ahead of the subcommand.
			name:      "k3 model flag precedes the acp subcommand",
			overrides: map[string]string{"model": "k3"},
			want:      "kimi --yolo --model kimi-code/k3 --no-thinking acp",
		},
		{
			name:      "k2.7-highspeed with thinking off precedes the acp subcommand",
			overrides: map[string]string{"model": "k2.7-highspeed", "thinking": "off"},
			want:      "kimi --yolo --model kimi-code/kimi-for-coding-highspeed --no-thinking acp",
		},
		{
			name:      "thinking on precedes the acp subcommand",
			overrides: map[string]string{"thinking": "on"},
			want:      "kimi --yolo --thinking acp",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildProviderLaunchCommand("", rp, tt.overrides, SessionTransportACP)
			if err != nil {
				t.Fatalf("BuildProviderLaunchCommand: %v", err)
			}
			if got.Command != tt.want {
				t.Fatalf("Command = %q, want %q", got.Command, tt.want)
			}
		})
	}
}

// TestBuildProviderLaunchCommandZeroOptionACPLaunchKeepsModernHandshake pins
// the no-option launch: nothing is inserted, and the command still ends in the
// modern `acp` subcommand rather than the deprecated global --acp flag (which
// kimi answers with an ACP invalid_params during initialize).
func TestBuildProviderLaunchCommandZeroOptionACPLaunchKeepsModernHandshake(t *testing.T) {
	spec := kimiCitySpec()
	rp := specToResolved("kimi", &spec)

	got, err := BuildProviderLaunchCommand("", rp, nil, SessionTransportACP)
	if err != nil {
		t.Fatalf("BuildProviderLaunchCommand: %v", err)
	}
	want := "kimi --yolo --no-thinking acp"
	if got.Command != want {
		t.Fatalf("Command = %q, want %q", got.Command, want)
	}
	if strings.Contains(got.Command, "--acp") {
		t.Fatalf("Command = %q, want the modern acp subcommand, not the deprecated --acp flag", got.Command)
	}
}

// TestBuildProviderLaunchCommandKeepsSettingsBeforeACPSubcommand pins the same
// invariant for the provider-owned settings file: --settings is a global
// option too, so it must land before the subcommand, not after it.
func TestBuildProviderLaunchCommandKeepsSettingsBeforeACPSubcommand(t *testing.T) {
	dir := t.TempDir()
	runtimeDir := filepath.Join(dir, ".gc")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(runtimeDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	rp := &ResolvedProvider{
		Name:          "claude",
		Command:       "claude",
		ACPArgs:       []string{"acp"},
		ACPSubcommand: "acp",
	}

	t.Run("with options", func(t *testing.T) {
		got, err := BuildProviderLaunchCommand(dir, rp, nil, SessionTransportACP)
		if err != nil {
			t.Fatalf("BuildProviderLaunchCommand: %v", err)
		}
		want := "claude --settings " + shellquote.Join([]string{settingsPath}) + " acp"
		if got.Command != want {
			t.Fatalf("Command = %q, want %q", got.Command, want)
		}
	})

	t.Run("without options", func(t *testing.T) {
		got, err := BuildProviderLaunchCommandWithoutOptions(dir, rp, SessionTransportACP)
		if err != nil {
			t.Fatalf("BuildProviderLaunchCommandWithoutOptions: %v", err)
		}
		want := "claude --settings " + shellquote.Join([]string{settingsPath}) + " acp"
		if got.Command != want {
			t.Fatalf("Command = %q, want %q", got.Command, want)
		}
		if got.SettingsPath != settingsPath {
			t.Fatalf("SettingsPath = %q, want %q", got.SettingsPath, settingsPath)
		}
	})
}

// TestBuildProviderLaunchCommandACPSubcommandLeavesOtherPathsAlone pins the
// blast radius: the placement rule only fires for the ACP transport, and only
// when the declared subcommand is a standalone token in the composed command.
func TestBuildProviderLaunchCommandACPSubcommandLeavesOtherPathsAlone(t *testing.T) {
	t.Run("tmux transport appends as before", func(t *testing.T) {
		spec := kimiCitySpec()
		rp := specToResolved("kimi", &spec)

		got, err := BuildProviderLaunchCommand("", rp, map[string]string{"model": "k3"}, SessionTransportTmux)
		if err != nil {
			t.Fatalf("BuildProviderLaunchCommand: %v", err)
		}
		want := "kimi --yolo --model kimi-code/k3 --no-thinking"
		if got.Command != want {
			t.Fatalf("Command = %q, want %q", got.Command, want)
		}
	})

	t.Run("shell trampoline without a standalone token appends as before", func(t *testing.T) {
		spec := kimiCitySpec()
		spec.ACPCommand = `sh -c 'exec kimi --yolo "$@" acp' --`
		spec.ACPArgs = []string{}
		rp := specToResolved("kimi", &spec)

		got, err := BuildProviderLaunchCommand("", rp, map[string]string{"model": "k3"}, SessionTransportACP)
		if err != nil {
			t.Fatalf("BuildProviderLaunchCommand: %v", err)
		}
		want := `sh -c 'exec kimi --yolo "$@" acp' -- --model kimi-code/k3 --no-thinking`
		if got.Command != want {
			t.Fatalf("Command = %q, want %q", got.Command, want)
		}
	})

	t.Run("provider without a declared subcommand appends as before", func(t *testing.T) {
		spec := kimiCitySpec()
		spec.ACPSubcommand = ""
		rp := specToResolved("kimi", &spec)

		got, err := BuildProviderLaunchCommand("", rp, map[string]string{"model": "k3"}, SessionTransportACP)
		if err != nil {
			t.Fatalf("BuildProviderLaunchCommand: %v", err)
		}
		want := "kimi --yolo acp --model kimi-code/k3 --no-thinking"
		if got.Command != want {
			t.Fatalf("Command = %q, want %q", got.Command, want)
		}
	})
}
