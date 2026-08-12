package main

import (
	"io"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// kimiACPBuildParams builds resolver inputs for a city whose kimi provider is
// the builtin profile. Kimi enters ACP through the `acp` subcommand, and the
// options gc manages (--model, --thinking) plus the hook --config-file are
// global options of the parent CLI that the subcommand's parser rejects.
func kimiACPBuildParams(t *testing.T, cityPath string) *agentBuildParams {
	t.Helper()
	return &agentBuildParams{
		cityName:   "city",
		cityPath:   cityPath,
		workspace:  &config.Workspace{Provider: "kimi"},
		providers:  map[string]config.ProviderSpec{"kimi": {Base: stringPtr("builtin:kimi")}},
		lookPath:   func(string) (string, error) { return "/usr/local/bin/kimi", nil },
		fs:         fsys.OSFS{},
		beaconTime: time.Unix(0, 0),
		beadNames:  make(map[string]string),
		stderr:     io.Discard,
	}
}

// TestResolveTemplateKeepsGlobalFlagsBeforeACPSubcommand pins the composition
// the reconciler actually launches: every global flag gc adds — the hook
// --config-file and the schema-managed defaults — lands before `acp`, and the
// launch keeps the modern subcommand handshake rather than the deprecated
// global --acp flag (which kimi answers with ACP invalid_params).
func TestResolveTemplateKeepsGlobalFlagsBeforeACPSubcommand(t *testing.T) {
	cityPath := t.TempDir()
	writeTemplateResolveCityConfig(t, cityPath, "file")

	agent := &config.Agent{
		Name:              "worker",
		Provider:          "kimi",
		Session:           "acp",
		InstallAgentHooks: []string{"kimi"},
		OptionDefaults:    map[string]string{"model": "kimi-k2.6"},
	}

	tp, err := resolveTemplate(kimiACPBuildParams(t, cityPath), agent, agent.QualifiedName(), nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}

	want := "kimi --yolo --no-thinking --config-file .kimi/config.toml --model kimi-k2.6 acp"
	if tp.Command != want {
		t.Fatalf("Command = %q, want %q", tp.Command, want)
	}
}

// TestResolveTemplateZeroOptionACPLaunchKeepsSubcommandHandshake pins the
// launch that composes nothing: the command is the provider's ACP command
// verbatim, still ending in the modern `acp` subcommand rather than the
// deprecated global --acp flag.
func TestResolveTemplateZeroOptionACPLaunchKeepsSubcommandHandshake(t *testing.T) {
	cityPath := t.TempDir()
	writeTemplateResolveCityConfig(t, cityPath, "file")

	agent := &config.Agent{Name: "worker", Provider: "kimi", Session: "acp"}

	tp, err := resolveTemplate(kimiACPBuildParams(t, cityPath), agent, agent.QualifiedName(), nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}

	want := "kimi --yolo --no-thinking acp"
	if tp.Command != want {
		t.Fatalf("Command = %q, want %q", tp.Command, want)
	}
}

// TestResolveTemplateTmuxTransportKeepsTrailingFlags pins the blast radius: the
// placement rule is ACP-only, so the tmux command keeps appending flags at the
// end where kimi's bare CLI parses them.
func TestResolveTemplateTmuxTransportKeepsTrailingFlags(t *testing.T) {
	cityPath := t.TempDir()
	writeTemplateResolveCityConfig(t, cityPath, "file")

	agent := &config.Agent{
		Name:              "worker",
		Provider:          "kimi",
		Session:           "tmux",
		InstallAgentHooks: []string{"kimi"},
		OptionDefaults:    map[string]string{"model": "kimi-k2.6"},
	}

	tp, err := resolveTemplate(kimiACPBuildParams(t, cityPath), agent, agent.QualifiedName(), nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}

	want := "kimi --yolo --no-thinking --config-file .kimi/config.toml --model kimi-k2.6"
	if tp.Command != want {
		t.Fatalf("Command = %q, want %q", tp.Command, want)
	}
}

// TestResolveTemplateShellTrampolineKeepsAppendPlacement pins the shape the
// live city runs until this fix ships in its installed binary: a provider-level
// shell trampoline that relocates the appended flags itself. The wrapper quotes
// `acp` inside a larger token, so gc appends and lets the wrapper place them.
func TestResolveTemplateShellTrampolineKeepsAppendPlacement(t *testing.T) {
	cityPath := t.TempDir()
	writeTemplateResolveCityConfig(t, cityPath, "file")

	params := kimiACPBuildParams(t, cityPath)
	params.providers = map[string]config.ProviderSpec{"kimi": {
		Base:       stringPtr("builtin:kimi"),
		ACPCommand: `sh -c 'exec kimi --yolo "$@" acp' --`,
		ACPArgs:    []string{},
	}}
	agent := &config.Agent{
		Name:              "worker",
		Provider:          "kimi",
		Session:           "acp",
		InstallAgentHooks: []string{"kimi"},
		OptionDefaults:    map[string]string{"model": "kimi-k2.6"},
	}

	tp, err := resolveTemplate(params, agent, agent.QualifiedName(), nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}

	want := `sh -c 'exec kimi --yolo "$@" acp' -- --config-file .kimi/config.toml --model kimi-k2.6`
	if tp.Command != want {
		t.Fatalf("Command = %q, want %q", tp.Command, want)
	}
}

// kimiACPResolvedProvider mirrors the live city's kimi provider after
// resolution: builtin ACP composition plus the city-side options_schema naming
// the provisioned model ids. Every flag in that schema is a kimi global option.
func kimiACPResolvedProvider() *config.ResolvedProvider {
	return &config.ResolvedProvider{
		Name:          "kimi",
		Command:       "kimi",
		ACPArgs:       []string{"--yolo", "--no-thinking", "acp"},
		ACPSubcommand: "acp",
		OptionsSchema: []config.ProviderOption{
			{
				Key:   "model",
				Label: "Model",
				Choices: []config.OptionChoice{
					{Value: "k3", FlagArgs: []string{"--model", "kimi-code/k3"}},
					{Value: "k2.7-highspeed", FlagArgs: []string{"--model", "kimi-code/kimi-for-coding-highspeed"}},
				},
			},
			{
				Key:   "thinking",
				Label: "Thinking",
				Choices: []config.OptionChoice{
					{Value: "on", FlagArgs: []string{"--thinking"}},
					{Value: "off", FlagArgs: []string{"--no-thinking"}},
				},
			},
		},
		EffectiveDefaults: map[string]string{"thinking": "off"},
	}
}

// TestApplyTemplateOverridesKeepsFlagsBeforeACPSubcommand covers the drift-hash
// half of the launch: per-session template_overrides re-place the schema flags
// on an already-composed ACP command, and must not push them past `acp`.
func TestApplyTemplateOverridesKeepsFlagsBeforeACPSubcommand(t *testing.T) {
	const baseCommand = "kimi --yolo --no-thinking acp"
	tests := []struct {
		name      string
		isACP     bool
		overrides string
		want      string
	}{
		{
			name:      "k3 override precedes the subcommand",
			isACP:     true,
			overrides: `{"model":"k3"}`,
			want:      "kimi --yolo --model kimi-code/k3 --no-thinking acp",
		},
		{
			name:      "k2.7-highspeed override precedes the subcommand",
			isACP:     true,
			overrides: `{"model":"k2.7-highspeed","thinking":"off"}`,
			want:      "kimi --yolo --model kimi-code/kimi-for-coding-highspeed --no-thinking acp",
		},
		{
			name:      "non-acp launch keeps trailing placement",
			isACP:     false,
			overrides: `{"model":"k3"}`,
			want:      "kimi --yolo acp --model kimi-code/k3 --no-thinking",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agentCfg := runtime.Config{Command: baseCommand}
			tp := TemplateParams{
				Command:          baseCommand,
				ResolvedProvider: kimiACPResolvedProvider(),
				IsACP:            tt.isACP,
			}
			applyTemplateOverridesToConfigInfo(&agentCfg, sessionpkg.Info{TemplateOverrides: tt.overrides}, tp)
			if agentCfg.Command != tt.want {
				t.Fatalf("Command = %q, want %q", agentCfg.Command, tt.want)
			}
		})
	}
}

// TestApplySchemaOptionOverridesForLaunchKeepsFlagsBeforeACPSubcommand covers
// the start half. It must agree with the drift-hash half exactly: a launch
// command that differs from the hashed one reads as permanent config drift and
// the reconciler restarts the session every cycle.
func TestApplySchemaOptionOverridesForLaunchKeepsFlagsBeforeACPSubcommand(t *testing.T) {
	const baseCommand = "kimi --yolo --no-thinking acp"
	overrides := map[string]string{"model": "k3"}
	want := "kimi --yolo --model kimi-code/k3 --no-thinking acp"

	launchCfg := runtime.Config{Command: baseCommand}
	launchTP := TemplateParams{Command: baseCommand, ResolvedProvider: kimiACPResolvedProvider(), IsACP: true}
	applySchemaOptionOverridesForLaunch(&launchCfg, &launchTP, "gc-1", overrides)
	if launchCfg.Command != want {
		t.Fatalf("launch Command = %q, want %q", launchCfg.Command, want)
	}

	hashCfg := runtime.Config{Command: baseCommand}
	hashTP := TemplateParams{Command: baseCommand, ResolvedProvider: kimiACPResolvedProvider(), IsACP: true}
	applyTemplateOverridesToConfigInfo(&hashCfg, sessionpkg.Info{TemplateOverrides: `{"model":"k3"}`}, hashTP)
	if hashCfg.Command != launchCfg.Command {
		t.Fatalf("drift-hash command %q disagrees with launch command %q", hashCfg.Command, launchCfg.Command)
	}
}
