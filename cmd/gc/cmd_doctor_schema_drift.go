package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads/contract"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
	beadsschema "github.com/steveyegge/beads/schema"
)

const schemaDriftProbeTimeout = 5 * time.Second

type schemaDriftStatus string

const (
	schemaDriftOK    schemaDriftStatus = "ok"
	schemaDriftError schemaDriftStatus = "error"
)

// schemaDriftObservation is one binary/store comparison. KnownExact is false
// when the binary only proved that it can read the observed store; this is the
// safe lower-bound reported by current bd versions, which do not expose their
// migration ceiling in `bd version --json`.
type schemaDriftObservation struct {
	Scope      string
	Binary     string
	Known      int
	KnownExact bool
	Store      int
	Error      string
}

type schemaDriftEvaluation struct {
	status  schemaDriftStatus
	message string
	details []string
}

func evaluateSchemaDrift(observations []schemaDriftObservation) schemaDriftEvaluation {
	if len(observations) == 0 {
		return schemaDriftEvaluation{status: schemaDriftOK, message: "no managed bd-backed Dolt stores found"}
	}

	result := schemaDriftEvaluation{status: schemaDriftOK}
	for _, observation := range observations {
		if observation.Error != "" {
			result.status = schemaDriftError
			result.details = append(result.details, fmt.Sprintf("%s %s: %s", observation.Scope, observation.Binary, observation.Error))
			continue
		}
		if observation.Known <= 0 || observation.Store < 0 {
			result.status = schemaDriftError
			result.details = append(result.details, fmt.Sprintf("%s %s: schema ceiling or store version is unavailable", observation.Scope, observation.Binary))
			continue
		}

		qualification := "supports"
		if !observation.KnownExact {
			qualification = "supports at least"
		}
		result.details = append(result.details, fmt.Sprintf("%s %s %s v%d; store at v%d", observation.Scope, observation.Binary, qualification, observation.Known, observation.Store))
		if observation.Known < observation.Store {
			result.status = schemaDriftError
			result.details = append(result.details, fmt.Sprintf("%s %s is behind store v%d by %d migration(s)", observation.Scope, observation.Binary, observation.Store, observation.Store-observation.Known))
		}
	}

	if result.status == schemaDriftError {
		result.message = "binary/schema drift detected"
	} else {
		result.message = "gc and bd schema ceilings cover every managed Dolt store"
	}
	return result
}

type schemaDriftCheck struct {
	cityPath string
	cfg      *config.City
}

func newSchemaDriftCheck(cityPath string, cfg *config.City) *schemaDriftCheck {
	return &schemaDriftCheck{cityPath: cityPath, cfg: cfg}
}

func (c *schemaDriftCheck) Name() string { return "schema-drift" }

func (c *schemaDriftCheck) CanFix() bool { return false }

func (c *schemaDriftCheck) Fix(_ *doctor.CheckContext) error { return nil }

func (c *schemaDriftCheck) WarmupEligible() bool { return true }

func (c *schemaDriftCheck) Run(_ *doctor.CheckContext) *doctor.CheckResult {
	result := &doctor.CheckResult{Name: c.Name()}
	if c.cfg == nil {
		result.Status = doctor.StatusOK
		result.Message = "city config unavailable; schema drift check skipped"
		return result
	}

	observations, err := c.observe()
	if err != nil {
		result.Status = doctor.StatusError
		result.Message = "schema drift probe failed"
		result.Details = []string{err.Error()}
		return result
	}
	evaluation := evaluateSchemaDrift(observations)
	result.Message = evaluation.message
	result.Details = evaluation.details
	if evaluation.status == schemaDriftError {
		result.Status = doctor.StatusError
		result.FixHint = "install a gc and bd build whose Beads schema ceiling is at least the highest store version"
	} else {
		result.Status = doctor.StatusOK
	}
	return result
}

type schemaDriftScope struct {
	name   string
	root   string
	target contract.DoltConnectionTarget
}

func (c *schemaDriftCheck) observe() ([]schemaDriftObservation, error) {
	scopes, err := c.scopes()
	if err != nil {
		return nil, err
	}
	var observations []schemaDriftObservation
	for _, scope := range scopes {
		storeVersion, err := readDoltSchemaVersion(scope.target)
		if err != nil {
			observations = append(observations,
				schemaDriftObservation{Scope: scope.name, Binary: "gc", Error: fmt.Sprintf("read store schema: %v", err)},
			)
			continue
		}
		observations = append(observations, schemaDriftObservation{
			Scope: scope.name, Binary: "gc", Known: beadsschema.LatestVersion(), KnownExact: true, Store: storeVersion,
		})

		bdObservation := c.observeBD(scope, storeVersion)
		observations = append(observations, bdObservation)
	}
	return observations, nil
}

func (c *schemaDriftCheck) scopes() ([]schemaDriftScope, error) {
	var scopes []schemaDriftScope
	add := func(name, root string) error {
		if !scopeUsesManagedBdStoreContract(c.cityPath, root) {
			return nil
		}
		target, ok, err := canonicalScopeDoltTarget(c.cityPath, root)
		if err != nil {
			return fmt.Errorf("resolve %s Dolt target: %w", name, err)
		}
		if !ok {
			return nil
		}
		scopes = append(scopes, schemaDriftScope{name: name, root: root, target: target})
		return nil
	}
	if err := add("city", c.cityPath); err != nil {
		return nil, err
	}
	for _, rig := range c.cfg.Rigs {
		if strings.TrimSpace(rig.Path) == "" {
			continue
		}
		root := resolveStoreScopeRoot(c.cityPath, rig.Path)
		if err := add("rig:"+rig.Name, root); err != nil {
			return nil, err
		}
	}
	return scopes, nil
}

func readDoltSchemaVersion(target contract.DoltConnectionTarget) (int, error) {
	db, err := managedDoltOpenDatabase(target.Host, target.Port, target.User, target.Database)
	if err != nil {
		return 0, err
	}
	defer db.Close() //nolint:errcheck // read-only probe

	ctx, cancel := context.WithTimeout(context.Background(), schemaDriftProbeTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return 0, err
	}
	version, err := beadsschema.CurrentVersion(ctx, db)
	if err != nil {
		return 0, err
	}
	return version, nil
}

func (c *schemaDriftCheck) observeBD(scope schemaDriftScope, storeVersion int) schemaDriftObservation {
	observation := schemaDriftObservation{Scope: scope.name, Binary: "bd", Store: storeVersion}
	var runner func(string, string, ...string) ([]byte, error)
	if scope.root == c.cityPath {
		runner = bdCommandRunnerForCity(c.cityPath)
	} else {
		runner = bdCommandRunnerForRig(c.cityPath, c.cfg, scope.root)
	}

	versionOutput, versionErr := runner(scope.root, "bd", "version", "--json")
	if versionErr == nil {
		if ceiling, err := parseBDSchemaCeiling(versionOutput); err == nil {
			observation.Known = ceiling
			observation.KnownExact = true
			return observation
		}
	}

	doctorOutput, doctorErr := runner(scope.root, "bd", "doctor", "--server", "--json")
	if skew, err := parseBDSchemaSkew(doctorOutput); err == nil {
		observation.Known = skew.required
		observation.KnownExact = true
		return observation
	}
	if doctorErr != nil {
		observation.Error = fmt.Sprintf("bd schema probe failed: %v", doctorErr)
		if versionErr != nil {
			observation.Error += fmt.Sprintf("; version probe failed: %v", versionErr)
		}
		return observation
	}

	// Current bd releases prove compatibility through `bd doctor --server`,
	// but do not print LatestVersion. Treat the observed store version as a
	// lower bound, preserving the distinction in the rendered detail.
	observation.Known = storeVersion
	return observation
}

func parseBDSchemaCeiling(data []byte) (int, error) {
	var payload struct {
		SchemaCeiling          int `json:"schema_ceiling"`
		KnownSchemaVersion     int `json:"known_schema_version"`
		SupportedSchemaVersion int `json:"supported_schema_version"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return 0, err
	}
	for _, version := range []int{payload.SchemaCeiling, payload.KnownSchemaVersion, payload.SupportedSchemaVersion} {
		if version > 0 {
			return version, nil
		}
	}
	return 0, errors.New("bd version output does not expose a schema ceiling")
}

type bdSchemaSkew struct {
	current  int
	required int
}

var bdSchemaSkewPattern = regexp.MustCompile(`(?i)database is at v([0-9]+), binary (?:knows up to|expects) v([0-9]+)`)

func parseBDSchemaSkew(data []byte) (bdSchemaSkew, error) {
	var payload struct {
		SchemaSkew struct {
			Current  int `json:"current_version"`
			Required int `json:"required_version"`
		} `json:"schema_skew"`
	}
	if err := json.Unmarshal(data, &payload); err == nil && payload.SchemaSkew.Current > 0 && payload.SchemaSkew.Required > 0 {
		return bdSchemaSkew{current: payload.SchemaSkew.Current, required: payload.SchemaSkew.Required}, nil
	}
	match := bdSchemaSkewPattern.FindStringSubmatch(string(data))
	if len(match) != 3 {
		return bdSchemaSkew{}, errors.New("bd output does not report schema skew")
	}
	var skew bdSchemaSkew
	if _, err := fmt.Sscanf(match[1], "%d", &skew.current); err != nil {
		return bdSchemaSkew{}, err
	}
	if _, err := fmt.Sscanf(match[2], "%d", &skew.required); err != nil {
		return bdSchemaSkew{}, err
	}
	return skew, nil
}
