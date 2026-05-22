# E2E-01 — Workspace init: scaffold + apply + doctor all-green

Tests the full polyforge init flow from scratch.

## Preconditions

- WORKSPACE_DIR: a fresh empty directory (e.g. /tmp/pf-test-init-XXXX)
- polyforge binary: /usr/local/bin/polyforge
- ~/.polyforge/config.toml: must exist with valid api_key + server.url

## Steps

### 1. Create .polyforge.yaml
NOTE: Write the following to WORKSPACE_DIR/.polyforge.yaml:
```yaml
version: 1
scenario: coding
aihub:
    url: http://10.146.0.16:8080
default_project: marketplace
projects:
    marketplace:
        repos:
            - name: marketplace
              url: git@github.com:GMISWE/GMI-marketplace.git
              github_owner_repo: GMISWE/GMI-marketplace
              description: Internal plugin marketplace.
```

### 2. polyforge init (scaffold)
CALL: bash POLYFORGE_WORKSPACE_ROOT=WORKSPACE_DIR polyforge init
ASSERT: output contains "ok .polyforge/phase.yaml written"
ASSERT: output contains "ok .polyforge/usage.md written"
ASSERT: output contains "ok CLAUDE.md"
ASSERT: file WORKSPACE_DIR/.polyforge/phase.yaml exists
ASSERT: file WORKSPACE_DIR/.polyforge/usage.md exists
ASSERT: file WORKSPACE_DIR/CLAUDE.md exists
ASSERT: WORKSPACE_DIR/CLAUDE.md contains "@.polyforge/usage.md"
ASSERT: WORKSPACE_DIR/.polyforge/usage.md contains "pf.<seq>.<ulid8>" (not "pf3.")

### 3. polyforge doctor (pre-apply — repos warn expected)
CALL: bash POLYFORGE_WORKSPACE_ROOT=WORKSPACE_DIR polyforge doctor
ASSERT: output contains "[ok] workspace"
ASSERT: output contains "[ok] config"
ASSERT: output contains "[warn] repos"
ASSERT: output contains "[ok] worktrees"
ASSERT: output contains "[ok] version"

### 4. polyforge init --apply (clone repos)
CALL: bash POLYFORGE_WORKSPACE_ROOT=WORKSPACE_DIR polyforge init --apply
ASSERT: output contains "ok cloned" OR output contains "already present" (idempotent)
ASSERT: output contains "ok CLAUDE.md managed block"
ASSERT: directory WORKSPACE_DIR/.repo/marketplace exists
ASSERT: WORKSPACE_DIR/CLAUDE.md contains "<!-- polyforge:managed"
ASSERT: WORKSPACE_DIR/CLAUDE.md contains "| marketplace |"

### 5. polyforge doctor (post-apply — all green)
CALL: bash POLYFORGE_WORKSPACE_ROOT=WORKSPACE_DIR polyforge doctor
ASSERT: output contains "[ok] workspace"
ASSERT: output contains "[ok] config"
ASSERT: output contains "[ok] repos"
ASSERT: output contains "[ok] worktrees"
ASSERT: output contains "[ok] version"
NOTE: No [warn] or [FAIL] lines expected

### 6. Idempotency: second --apply does not re-clone
CALL: bash POLYFORGE_WORKSPACE_ROOT=WORKSPACE_DIR polyforge init --apply
ASSERT: output does NOT contain "Cloning"
ASSERT: output contains "already present" OR "managed block updated"

## Cleanup

Remove WORKSPACE_DIR recursively.
Remove cloned .repo/marketplace worktrees with:
  git -C WORKSPACE_DIR/.repo/marketplace worktree prune

## PASS criteria

init scaffold creates all 3 files; usage.md uses v1 naming;
--apply clones repos + writes managed block; doctor all-green after apply;
second --apply is idempotent.
