-- +goose Up
-- aihub#222: file_scope lock keys are now namespaced by the owning wi's project
-- ("<project>:<path>") so that byte-identical relative paths in different projects
-- (e.g. an ieops-v2 fork under a separate project and the parent repo) no longer
-- share a lock key and hard-block each other. Rewrite existing file_scope rows to
-- the namespaced form using the project of the wi that owns the holding attempt.
-- git_branch keys (repo/branch) and deploy_env keys (service) are left unchanged:
-- git_branch is already repo-qualified, and deploy_env is intentionally global so
-- cross-project deploys to one environment still conflict.
UPDATE resource_locks rl
SET resource_key = wi.project || ':' || rl.resource_key
FROM run_attempts ra
JOIN work_items wi ON wi.id = ra.work_item_id
WHERE rl.owner_attempt_id = ra.id
  AND rl.resource_type = 'file_scope'
  -- Idempotency guard: never double-prefix a key that is already namespaced
  -- (e.g. a new binary that inserted "<project>:<path>" before this ran).
  AND rl.resource_key NOT LIKE wi.project || ':%';

-- +goose Down
-- Strip the "<project>:" prefix from file_scope keys, restoring the bare path.
UPDATE resource_locks rl
SET resource_key = substring(rl.resource_key FROM char_length(wi.project) + 2)
FROM run_attempts ra
JOIN work_items wi ON wi.id = ra.work_item_id
WHERE rl.owner_attempt_id = ra.id
  AND rl.resource_type = 'file_scope'
  AND rl.resource_key LIKE wi.project || ':%';
