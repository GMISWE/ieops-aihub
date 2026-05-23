-- +goose Up

-- Step 1: backfill projects from work_items/memories DISTINCT project
DO $$
DECLARE fallback_admin TEXT;
BEGIN
    SELECT id INTO fallback_admin FROM users WHERE role='admin' ORDER BY created_at ASC LIMIT 1;
    IF fallback_admin IS NULL THEN
        RAISE EXCEPTION 'no admin user exists; bootstrap admin first';
    END IF;
    INSERT INTO projects(name, owner_user_id, visible, scenario)
    SELECT DISTINCT project, fallback_admin, true, 'coding'
    FROM (SELECT project FROM work_items UNION SELECT project FROM memories) p
    ON CONFLICT(name) DO NOTHING;
END $$;

-- Step 2: users.project_roles keys → projects table (ensure all referenced projects exist)
INSERT INTO projects(name, owner_user_id, visible, scenario)
SELECT key, (SELECT id FROM users WHERE role='admin' ORDER BY created_at LIMIT 1), true, 'coding'
FROM users, jsonb_each(project_roles)
WHERE NOT EXISTS (SELECT 1 FROM projects WHERE name=key)
ON CONFLICT(name) DO NOTHING;

-- members migration: copy users.project_roles into projects.members JSONB
DO $$
DECLARE rec RECORD;
BEGIN
    FOR rec IN SELECT u.id as user_id, (pr).key as project_name, (pr).value#>>'{}' as role
               FROM users u, jsonb_each(u.project_roles) pr
               WHERE (pr).value#>>'{}' IN ('viewer','writer','maintainer')
    LOOP
        IF EXISTS (SELECT 1 FROM projects WHERE name=rec.project_name) AND
           EXISTS (SELECT 1 FROM users WHERE id=rec.user_id) THEN
            UPDATE projects
            SET members = members || jsonb_build_array(jsonb_build_object(
                'user_id', rec.user_id,
                'role', CASE WHEN rec.role='maintainer' THEN 'writer' ELSE rec.role END
            ))
            WHERE name=rec.project_name
              AND NOT members @> jsonb_build_array(jsonb_build_object('user_id', rec.user_id));
        ELSE
            RAISE WARNING 'stale project_roles entry: user=% project=%', rec.user_id, rec.project_name;
        END IF;
    END LOOP;
END $$;

-- Step 3: FK constraints from work_items/memories → projects
ALTER TABLE work_items ADD CONSTRAINT fk_wi_project
    FOREIGN KEY (project) REFERENCES projects(name) ON UPDATE RESTRICT ON DELETE RESTRICT;
ALTER TABLE memories ADD CONSTRAINT fk_mem_project
    FOREIGN KEY (project) REFERENCES projects(name) ON UPDATE RESTRICT ON DELETE RESTRICT;

-- +goose Down
-- Remove FK constraints added by this migration first, then remove all backfilled projects.
-- This fully reverts 0013: the projects table was empty before this migration ran.
ALTER TABLE work_items DROP CONSTRAINT IF EXISTS fk_wi_project;
ALTER TABLE memories DROP CONSTRAINT IF EXISTS fk_mem_project;
DELETE FROM projects;
