package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

func TestEvaluateSchemaDriftReportsStaleBinary(t *testing.T) {
	observations := []schemaDriftObservation{
		{Scope: "city", Binary: "gc", Known: 53, Store: 59},
		{Scope: "city", Binary: "bd", Known: 59, Store: 59},
	}

	result := evaluateSchemaDrift(observations)

	if result.status != schemaDriftError {
		t.Fatalf("status = %v, want error; message=%q details=%v", result.status, result.message, result.details)
	}
	joined := result.message + " " + strings.Join(result.details, " ")
	for _, want := range []string{"city", "gc", "v53", "v59", "behind"} {
		if !strings.Contains(joined, want) {
			t.Errorf("result = %q, want %q", joined, want)
		}
	}
}

func TestEvaluateSchemaDriftReportsEveryScope(t *testing.T) {
	observations := []schemaDriftObservation{
		{Scope: "city", Binary: "gc", Known: 59, Store: 59},
		{Scope: "rig:alpha", Binary: "gc", Known: 58, Store: 59},
		{Scope: "rig:alpha", Binary: "bd", Known: 57, Store: 59},
	}

	result := evaluateSchemaDrift(observations)

	joined := strings.Join(result.details, " ")
	for _, want := range []string{"rig:alpha", "gc", "v58", "bd", "v57"} {
		if !strings.Contains(joined, want) {
			t.Errorf("details = %q, want %q", joined, want)
		}
	}
}

func TestEvaluateSchemaDriftClean(t *testing.T) {
	result := evaluateSchemaDrift([]schemaDriftObservation{
		{Scope: "city", Binary: "gc", Known: 59, Store: 59},
		{Scope: "city", Binary: "bd", Known: 59, Store: 59},
	})

	if result.status != schemaDriftOK {
		t.Fatalf("status = %v, want OK; message=%q", result.status, result.message)
	}
	if !strings.Contains(result.message, "gc") || !strings.Contains(result.message, "bd") {
		t.Fatalf("message = %q, want both binaries", result.message)
	}
}

func TestParseBDSchemaCeiling(t *testing.T) {
	got, err := parseBDSchemaCeiling([]byte(`{"version":"1.2.1","schema_ceiling":59}`))
	if err != nil {
		t.Fatalf("parseBDSchemaCeiling() error = %v", err)
	}
	if got != 59 {
		t.Fatalf("schema ceiling = %d, want 59", got)
	}
}

func TestParseBDSchemaSkew(t *testing.T) {
	got, err := parseBDSchemaSkew([]byte(`{"error":"schema version mismatch: database is at v59, binary knows up to v53 (6 migrations ahead)"}`))
	if err != nil {
		t.Fatalf("parseBDSchemaSkew() error = %v", err)
	}
	if got.current != 59 || got.required != 53 {
		t.Fatalf("skew = %+v, want current=59 required=53", got)
	}
}

func TestBuildDoctorChecksRegistersSchemaDrift(t *testing.T) {
	cityDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte("[workspace]\nname = \"demo\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GC_DOLT", "skip")
	cfg := &config.City{Workspace: config.Workspace{Name: "demo"}}

	names := doctorCheckNames(buildDoctorChecks(cityDir, cfg, nil, buildDoctorChecksOpts{
		ControllerRunning:    false,
		SkipCityDoltCheck:    true,
		SkipManagedDoltCheck: true,
	}))

	if doctorCheckIndex(names, "schema-drift") < 0 {
		t.Fatalf("schema-drift check missing from %v", names)
	}
}

func TestSchemaDriftCheckIsWarmupEligible(t *testing.T) {
	if !newSchemaDriftCheck("", &config.City{}).WarmupEligible() {
		t.Fatal("schema-drift check must run during patrol warmup")
	}
}
