package domain

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ScenarioPhaseConfig represents a row from scenario_phase_configs.
type ScenarioPhaseConfig struct {
	Scenario  string          `json:"scenario"`
	Content   json.RawMessage `json:"content"` // {wi_types:{...}, classification_rules:[...]}
	Version   int             `json:"version"`
	UpdatedAt string          `json:"updated_at"`
	UpdatedBy *string         `json:"updated_by,omitempty"`
}

// UpdateScenarioConfigRequest is the body for PUT /v1/scenarios/:scenario/phase_config.
type UpdateScenarioConfigRequest struct {
	Content json.RawMessage `json:"content"`
	Version int             `json:"version"` // CAS expected version
}

// GetScenarioConfig fetches the phase config for a named scenario.
// Returns ErrNotFound if no config exists for this scenario.
func GetScenarioConfig(ctx context.Context, pool *pgxpool.Pool, scenario string) (*ScenarioPhaseConfig, error) {
	cfg := &ScenarioPhaseConfig{}
	err := pool.QueryRow(ctx, `
		SELECT scenario, content, version,
		       to_char(updated_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS updated_at,
		       updated_by
		FROM scenario_phase_configs
		WHERE scenario = $1`, scenario,
	).Scan(&cfg.Scenario, &cfg.Content, &cfg.Version, &cfg.UpdatedAt, &cfg.UpdatedBy)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, NewErr(ErrNotFound,
				fmt.Sprintf("no phase config found for scenario %q", scenario))
		}
		return nil, NewErr(ErrInternalError, fmt.Sprintf("failed to load scenario config: %v", err))
	}
	return cfg, nil
}

// UpdateScenarioConfig updates scenario_phase_configs with CAS version check.
//
// C-R9-2: 409 on version mismatch — no auto-merge allowed; client must GET + human-review + retry.
// C-R9-4: validates classification_rules[].set.wi_type ∈ wi_types keys; 400 on violation.
//
// Side effect: emits phase_config_updated event.
func UpdateScenarioConfig(ctx context.Context, pool *pgxpool.Pool,
	scenario string, req *UpdateScenarioConfigRequest, callerUserID string) (*ScenarioPhaseConfig, error) {

	// Validate content structure (C-R9-4)
	if err := validatePhaseContent(req.Content); err != nil {
		return nil, err
	}

	// CAS update: only proceed if version matches
	cfg := &ScenarioPhaseConfig{}
	err := pool.QueryRow(ctx, `
		UPDATE scenario_phase_configs
		SET content    = $1,
		    version    = version + 1,
		    updated_at = clock_timestamp(),
		    updated_by = $2
		WHERE scenario = $3 AND version = $4
		RETURNING scenario, content, version,
		          to_char(updated_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		          updated_by`,
		req.Content, callerUserID, scenario, req.Version,
	).Scan(&cfg.Scenario, &cfg.Content, &cfg.Version, &cfg.UpdatedAt, &cfg.UpdatedBy)

	if err != nil {
		if err == pgx.ErrNoRows {
			// Either scenario doesn't exist or version mismatch.
			// Distinguish by checking current version.
			var currentVersion int
			lookupErr := pool.QueryRow(ctx,
				`SELECT version FROM scenario_phase_configs WHERE scenario = $1`, scenario).
				Scan(&currentVersion)
			if lookupErr == pgx.ErrNoRows {
				return nil, NewErr(ErrNotFound,
					fmt.Sprintf("no phase config found for scenario %q", scenario))
			}
			// C-R9-2: CAS conflict → 409, human must review diff
			return nil, NewErrDetails(ErrConflictVersionMismatch,
				"version mismatch: GET the latest version, review the diff, then retry",
				map[string]any{"current_version": currentVersion, "expected_version": req.Version},
			)
		}
		return nil, NewErr(ErrInternalError, fmt.Sprintf("failed to update scenario config: %v", err))
	}

	// Emit phase_config_updated event
	payload, _ := json.Marshal(map[string]any{
		"scenario":    scenario,
		"old_version": req.Version,
		"new_version": cfg.Version,
		"changed_by":  callerUserID,
	})
	pool.Exec(ctx, `
		INSERT INTO agent_events (id, actor_user_id, event_type, payload, project, created_at)
		VALUES ($1, $2, 'phase_config_updated', $3, $4, clock_timestamp())`,
		NewID("evt"), callerUserID, payload, scenario,
	) //nolint:errcheck

	return cfg, nil
}

// validatePhaseContent validates the phase config content per C-R9-4.
// Ensures classification_rules[].set.wi_type ∈ wi_types keys.
func validatePhaseContent(content json.RawMessage) error {
	if len(content) == 0 {
		return NewErr(ErrInvalidPhaseYAML, "content is required")
	}

	var parsed struct {
		WITypes             map[string]json.RawMessage `json:"wi_types"`
		ClassificationRules []struct {
			Set *struct {
				WIType string `json:"wi_type"`
			} `json:"set,omitempty"`
		} `json:"classification_rules"`
	}

	if err := json.Unmarshal(content, &parsed); err != nil {
		return NewErr(ErrInvalidPhaseYAML, fmt.Sprintf("content is not valid JSON: %v", err))
	}

	// C-R9-4: each classification rule's wi_type must exist in wi_types
	for i, rule := range parsed.ClassificationRules {
		if rule.Set == nil || rule.Set.WIType == "" {
			continue
		}
		if _, ok := parsed.WITypes[rule.Set.WIType]; !ok {
			available := make([]string, 0, len(parsed.WITypes))
			for k := range parsed.WITypes {
				available = append(available, k)
			}
			return NewErrDetails(ErrInvalidPhaseYAML,
				fmt.Sprintf("classification_rules[%d].set.wi_type %q not found in wi_types", i, rule.Set.WIType),
				map[string]any{
					"wi_type":   rule.Set.WIType,
					"available": available,
				},
			)
		}
	}
	return nil
}
