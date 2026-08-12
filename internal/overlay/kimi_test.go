package overlay

import (
	"strings"
	"testing"
)

func TestMergeKimiConfigTOML_MergesInlineHooksArray(t *testing.T) {
	base := []byte(`
hooks = []
default_model = "kimi-code/k3"

[providers.kimi]
type = "kimi"
base_url = "https://api.kimi.example/v1"
api_key = "test-key"

[models."kimi-code/k3"]
provider = "kimi"
model = "kimi-code/k3"
max_context_size = 128000
`)
	overlay := []byte(`
[[hooks]]
event = "SessionStart"
command = "python3 .kimi/hooks/gascity-session-start.py"
timeout = 30
`)

	merged, err := MergeKimiConfigTOML(base, overlay)
	if err != nil {
		t.Fatalf("MergeKimiConfigTOML: %v", err)
	}
	if !KimiConfigHasLLM(merged) {
		t.Fatalf("merged config no longer has an LLM:\n%s", merged)
	}
	document, err := parseKimiConfig("merged", merged)
	if err != nil {
		t.Fatalf("parse merged config: %v", err)
	}
	if len(document.Hooks) != 1 {
		t.Fatalf("merged hooks = %#v, want one hook", document.Hooks)
	}
	if got := document.Hooks[0]["command"]; got != "python3 .kimi/hooks/gascity-session-start.py" {
		t.Fatalf("merged hook command = %#v", got)
	}
	if strings.Count(string(merged), "gascity-session-start.py") != 1 {
		t.Fatalf("managed hook occurs more than once:\n%s", merged)
	}
}

func TestKimiConfigHasLLM(t *testing.T) {
	fullConfig := []byte(`
default_model = "kimi-code/k3"

[providers.kimi]
type = "kimi"
base_url = "https://api.kimi.example/v1"
api_key = "test-key"

[models."kimi-code/k3"]
provider = "kimi"
model = "kimi-code/k3"
max_context_size = 128000
`)

	if !KimiConfigHasLLM(fullConfig) {
		t.Fatal("KimiConfigHasLLM(full config) = false, want true")
	}

	for name, data := range map[string][]byte{
		"hooks only": []byte(`
[[hooks]]
event = "SessionStart"
command = "python3 .kimi/hooks/gascity-session-start.py"
`),
		"unknown default model": []byte(`
default_model = "missing"

[providers.kimi]
type = "kimi"

[models.k3]
provider = "kimi"
`),
		"missing provider": []byte(`
default_model = "k3"

[models.k3]
provider = "kimi"
`),
		"incomplete provider and model": []byte(`
default_model = "k3"

[providers.kimi]
type = "kimi"
base_url = "https://api.kimi.example/v1"

[models.k3]
provider = "kimi"
model = "kimi-code/k3"
`),
		"malformed": []byte("[models\n"),
	} {
		t.Run(name, func(t *testing.T) {
			if KimiConfigHasLLM(data) {
				t.Fatal("KimiConfigHasLLM() = true, want false")
			}
		})
	}
}

func TestKimiConfigHasLaunchModel(t *testing.T) {
	config := []byte(`
[providers.kimi]
type = "kimi"
base_url = "https://api.kimi.example/v1"
api_key = "test-key"

[models."kimi-code/k3"]
provider = "kimi"
model = "kimi-code/k3"
max_context_size = 128000
`)

	if !KimiConfigHasLaunchModel(config, "kimi-code/k3") {
		t.Fatal("KimiConfigHasLaunchModel() = false, want true for the explicitly selected model")
	}
	if KimiConfigHasLaunchModel(config, "missing") {
		t.Fatal("KimiConfigHasLaunchModel() = true, want false for an unknown selected model")
	}
}
