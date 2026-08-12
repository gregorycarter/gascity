package main

import (
	"strings"

	"github.com/gastownhall/gascity/internal/config"
)

// replaceSchemaFlags strips all CLI flags associated with the provider's
// OptionsSchema from the command, then appends the given override flags.
func replaceSchemaFlags(command string, schema []config.ProviderOption, overrideArgs []string) string {
	return config.ReplaceSchemaFlags(command, schema, overrideArgs)
}

// replaceSchemaFlagsForTemplate strips the flags managed by the launch
// provider's OptionsSchema from command, then re-places overrideArgs where that
// provider parses them: before the ACP subcommand token for providers whose
// managed options are global options of the parent CLI, appended at the end
// otherwise.
//
// Both halves of a launch — the command that starts the session and the one the
// drift hash is computed over — must go through this helper. A disagreement
// between them reads as permanent config drift and restarts the session every
// reconcile cycle.
func replaceSchemaFlagsForTemplate(command string, tp TemplateParams, overrideArgs []string) string {
	if tp.ResolvedProvider == nil {
		return command
	}
	return config.ReplaceSchemaFlagsBeforeSubcommand(command, tp.ResolvedProvider.OptionsSchema, overrideArgs, templateACPSubcommand(tp))
}

// templateACPSubcommand returns the subcommand token that composed option flags
// must precede for this launch, or "" when the launch is not ACP or the provider
// does not declare one.
func templateACPSubcommand(tp TemplateParams) string {
	if !tp.IsACP || tp.ResolvedProvider == nil {
		return ""
	}
	return strings.TrimSpace(tp.ResolvedProvider.ACPSubcommand)
}

// stripFlags removes known flag sequences from a tokenized command.
func stripFlags(command string, flags [][]string) string {
	return config.StripFlags(command, flags)
}
