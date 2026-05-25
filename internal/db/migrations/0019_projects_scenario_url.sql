-- +goose Up
-- Change projects.scenario from enum ('coding'/'writing'/'data') to free-form TEXT NULL
-- Semantics: previously "scenario type", now "scenario repo URL" (e.g. git@github.com:GMISWE/polyforge-coding.git)
ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_scenario_check;
ALTER TABLE projects ALTER COLUMN scenario DROP NOT NULL;
ALTER TABLE projects ALTER COLUMN scenario DROP DEFAULT;

-- +goose Down
ALTER TABLE projects ALTER COLUMN scenario SET NOT NULL;
ALTER TABLE projects ALTER COLUMN scenario SET DEFAULT 'coding';
ALTER TABLE projects ADD CONSTRAINT projects_scenario_check CHECK (scenario IN ('coding','writing','data'));
