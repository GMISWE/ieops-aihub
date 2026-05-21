-- +goose Up
INSERT INTO scenario_phase_configs (scenario, content, version, updated_by) VALUES (
  'coding',
  '{
    "wi_types": {
      "fix_bug": {
        "requires_human_session": false,
        "steps": ["prepare_context", "code_change", "commit_and_pr"]
      },
      "critical_bug": {
        "requires_human_session": true,
        "steps": ["prepare_context", "spec", "code_change", "commit_and_pr"]
      },
      "feature": {
        "requires_human_session": true,
        "steps": ["spec", "plan", "code_change", "commit_and_pr", "review"]
      },
      "chore": {
        "requires_human_session": false,
        "steps": ["code_change", "commit_and_pr"]
      }
    },
    "classification_rules": [
      {"priority": "urgent", "wi_type_prefix": "critical", "set": {"wi_type": "critical_bug", "requires_human_session": true}},
      {"priority": "normal", "wi_type_prefix": "fix", "set": {"wi_type": "fix_bug", "requires_human_session": false}},
      {"priority": "normal", "wi_type_prefix": "feature", "set": {"wi_type": "feature", "requires_human_session": true}},
      {"priority": "normal", "wi_type_prefix": "chore", "set": {"wi_type": "chore", "requires_human_session": false}}
    ]
  }',
  1,
  NULL
) ON CONFLICT (scenario) DO NOTHING;

-- +goose Down
DELETE FROM scenario_phase_configs WHERE scenario = 'coding';
