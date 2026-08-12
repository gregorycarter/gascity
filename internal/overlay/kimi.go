package overlay

import (
	"bytes"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"
)

var kimiConfigPath = filepath.Join(".kimi", "config.toml")

type kimiConfigDocument struct {
	DefaultModel string                     `toml:"default_model"`
	Models       map[string]kimiConfigModel `toml:"models"`
	Providers    map[string]map[string]any  `toml:"providers"`
	Hooks        []map[string]any           `toml:"hooks"`
}

type kimiConfigModel struct {
	Provider       string `toml:"provider"`
	Model          string `toml:"model"`
	MaxContextSize int64  `toml:"max_context_size"`
}

func isKimiConfigPath(relPath string) bool {
	return filepath.Clean(relPath) == kimiConfigPath
}

// MergeKimiConfigTOML preserves an existing Kimi configuration and appends
// each missing hook from the overlay. Kimi loads one config file when
// --config-file is set, so replacing a full providers/models configuration
// with a hooks-only overlay leaves the CLI without an LLM.
func MergeKimiConfigTOML(base, overlay []byte) ([]byte, error) {
	baseDoc, err := parseKimiConfig("base", base)
	if err != nil {
		return nil, err
	}
	overlayDoc, err := parseKimiConfig("overlay", overlay)
	if err != nil {
		return nil, err
	}

	missing := make([]map[string]any, 0, len(overlayDoc.Hooks))
	for _, desired := range overlayDoc.Hooks {
		if kimiConfigHasHook(baseDoc.Hooks, desired) {
			continue
		}
		missing = append(missing, desired)
		baseDoc.Hooks = append(baseDoc.Hooks, desired)
	}
	if len(missing) == 0 {
		return append([]byte(nil), base...), nil
	}
	if hasKimiInlineHooks(base) {
		return encodeKimiConfigWithHooks(base, baseDoc.Hooks)
	}

	var hookTOML bytes.Buffer
	if err := toml.NewEncoder(&hookTOML).Encode(kimiConfigDocument{Hooks: missing}); err != nil {
		return nil, fmt.Errorf("encoding Kimi hooks: %w", err)
	}

	merged := make([]byte, 0, len(base)+1+hookTOML.Len())
	merged = append(merged, base...)
	if len(merged) > 0 && merged[len(merged)-1] != '\n' {
		merged = append(merged, '\n')
	}
	merged = append(merged, hookTOML.Bytes()...)
	if _, err := parseKimiConfig("merged", merged); err != nil {
		return nil, err
	}
	return merged, nil
}

// hasKimiInlineHooks recognizes Kimi's generated `hooks = []` form. Appending
// [[hooks]] to that form is invalid TOML because the key has already been
// defined, so callers must instead rewrite the parsed document with a single
// hooks collection.
func hasKimiInlineHooks(data []byte) bool {
	inTable := false
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inTable = true
			continue
		}
		if inTable {
			continue
		}
		key, _, found := strings.Cut(line, "=")
		if found && strings.TrimSpace(key) == "hooks" {
			return true
		}
	}
	return false
}

func encodeKimiConfigWithHooks(base []byte, hooks []map[string]any) ([]byte, error) {
	var document map[string]any
	if _, err := toml.Decode(string(base), &document); err != nil {
		return nil, fmt.Errorf("parsing base Kimi config for hook rewrite: %w", err)
	}
	document["hooks"] = hooks

	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(document); err != nil {
		return nil, fmt.Errorf("encoding Kimi config with hooks: %w", err)
	}
	merged := encoded.Bytes()
	if _, err := parseKimiConfig("merged", merged); err != nil {
		return nil, err
	}
	return merged, nil
}

// KimiConfigHasLLM reports whether data selects a configured default model.
// A hooks-only Kimi overlay is syntactically valid TOML but cannot launch an
// LLM when passed through --config-file, so callers use this to retain Kimi's
// default config unless the projected workdir config is complete.
func KimiConfigHasLLM(data []byte) bool {
	return KimiConfigHasLaunchModel(data, "")
}

// KimiConfigHasLaunchModel reports whether data configures selectedModel, or
// its default model when selectedModel is empty, with a complete provider
// mapping. Kimi accepts --model as an override of default_model, so a complete
// config does not need default_model when the launch command explicitly names
// a configured model.
func KimiConfigHasLaunchModel(data []byte, selectedModel string) bool {
	document, err := parseKimiConfig("launch", data)
	if err != nil {
		return false
	}
	modelName := strings.TrimSpace(selectedModel)
	if modelName == "" {
		modelName = strings.TrimSpace(document.DefaultModel)
	}
	if modelName == "" {
		return false
	}
	model, ok := document.Models[modelName]
	if !ok || strings.TrimSpace(model.Provider) == "" || strings.TrimSpace(model.Model) == "" || model.MaxContextSize <= 0 {
		return false
	}
	provider, ok := document.Providers[strings.TrimSpace(model.Provider)]
	if !ok {
		return false
	}
	return kimiProviderIsConfigured(provider)
}

func kimiProviderIsConfigured(provider map[string]any) bool {
	if strings.TrimSpace(kimiConfigString(provider["type"])) == "" || strings.TrimSpace(kimiConfigString(provider["base_url"])) == "" {
		return false
	}
	_, hasAPIKey := provider["api_key"].(string)
	return hasAPIKey
}

func kimiConfigString(value any) string {
	text, _ := value.(string)
	return text
}

func parseKimiConfig(label string, data []byte) (kimiConfigDocument, error) {
	var document kimiConfigDocument
	if _, err := toml.Decode(string(data), &document); err != nil {
		return kimiConfigDocument{}, fmt.Errorf("parsing %s Kimi config: %w", label, err)
	}
	return document, nil
}

func kimiConfigHasHook(existing []map[string]any, desired map[string]any) bool {
	desiredEvent, desiredEventOK := desired["event"].(string)
	desiredCommand, desiredCommandOK := desired["command"].(string)
	if desiredEventOK && desiredCommandOK && strings.TrimSpace(desiredEvent) != "" && strings.TrimSpace(desiredCommand) != "" {
		for _, hook := range existing {
			event, eventOK := hook["event"].(string)
			command, commandOK := hook["command"].(string)
			if eventOK && commandOK && event == desiredEvent && command == desiredCommand {
				return true
			}
		}
	}
	for _, hook := range existing {
		if reflect.DeepEqual(hook, desired) {
			return true
		}
	}
	return false
}
