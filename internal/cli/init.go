package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/pkg/client"
	"gopkg.in/yaml.v3"
)

// RunInit fetches the scenario phase config from aihub and writes it to
// <wsRoot>/.polyforge/phase.yaml.
//
// With --apply: also applies the local .polyforge/phase.yaml back to aihub
// via a PATCH (CAS update, using the embedded __version__ field).
func RunInit(ctx context.Context, c *client.Client, cfg *config.Config, wsRoot string, args []string) {
	apply := len(args) > 0 && args[0] == "--apply"

	// Determine scenario from config (or default to "coding").
	scenario := "coding"
	if cfg != nil && cfg.Scenario != "" {
		scenario = cfg.Scenario
	}

	phaseDir := filepath.Join(wsRoot, ".polyforge")
	phaseFile := filepath.Join(phaseDir, "phase.yaml")

	if apply {
		runInitApply(ctx, c, scenario, phaseFile)
		return
	}

	// GET /v1/scenario_configs/:scenario
	result, err := c.GetScenarioConfig(ctx, scenario)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pf init: failed to fetch scenario config: %v\n", err)
		os.Exit(1)
	}

	// Write to .polyforge/phase.yaml in the workspace root.
	if err := os.MkdirAll(phaseDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "pf init: mkdir .polyforge: %v\n", err)
		os.Exit(1)
	}

	// Embed version for CAS on future --apply.
	if v, ok := result["version"]; ok {
		result["__version__"] = v
	}

	b, err := yaml.Marshal(result)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pf init: marshal phase.yaml: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(phaseFile, b, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "pf init: write phase.yaml: %v\n", err)
		os.Exit(1)
	}

	ver, _ := result["version"]
	fmt.Printf("ok .polyforge/phase.yaml written (scenario: %s, version: %v)\n", scenario, ver)
}

// runInitApply reads the local phase.yaml and PATCHes it back to aihub (CAS).
func runInitApply(ctx context.Context, c *client.Client, scenario, phaseFile string) {
	b, err := os.ReadFile(phaseFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pf init --apply: read phase.yaml: %v\n", err)
		os.Exit(1)
	}

	var content map[string]any
	if err := yaml.Unmarshal(b, &content); err != nil {
		fmt.Fprintf(os.Stderr, "pf init --apply: parse phase.yaml: %v\n", err)
		os.Exit(1)
	}

	// Extract embedded __version__ for CAS.
	// Server schema (UpdateScenarioConfigRequest) reads the field as "version" (§4.3 PUT body).
	expectedVersion, _ := content["__version__"]
	delete(content, "__version__")

	// Normalize version to int for the server's CAS check (handles yaml numeric decoding).
	var versionInt int
	switch v := expectedVersion.(type) {
	case int:
		versionInt = v
	case int64:
		versionInt = int(v)
	case float64:
		versionInt = int(v)
	}

	body := map[string]any{
		"content": content,
		"version": versionInt,
	}

	result, err := c.UpdateScenarioConfig(ctx, scenario, body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pf init --apply: PATCH scenario config: %v\n", err)
		os.Exit(1)
	}

	newVer, _ := result["version"]
	fmt.Printf("ok phase.yaml applied to aihub (scenario: %s, version: %v)\n", scenario, newVer)

	// Update the local file with the new version.
	content["__version__"] = newVer
	updated, _ := yaml.Marshal(content)
	_ = os.WriteFile(phaseFile, updated, 0644)
}

