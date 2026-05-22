# E2E-14 — Doctor detects client version behind server min_client_version

Tests that `polyforge doctor` returns `[warn] version` when the local binary
version is behind the server's min_client_version requirement.

NOTE: This scenario is difficult to test without controlling the server's
min_client_version setting. Two approaches:

## Approach A — Update server min_client_version above local binary version

AS ADMIN: HTTP PATCH /v1/config (or similar admin endpoint to set min_client_version)
body: {"min_client_version": "999.0.0"}  (a version the local binary will never match)

Bash: POLYFORGE_WORKSPACE_ROOT= /usr/local/bin/polyforge doctor
ASSERT: output contains "[warn] version" or "[FAIL] version"
ASSERT: output mentions version mismatch or "update"

Cleanup: Reset min_client_version back to "1.0.0"

## Approach B — Build a fake old binary version (complex, skip for now)

NOTE: If admin config endpoint not available, this test requires either:
- A way to set POLYFORGE_VERSION env var (if binary reads it)
- A mock server that returns a higher min_client_version
- Direct DB update: UPDATE config SET min_client_version='999.0.0'

## Current limitation
Without the ability to control server-side min_client_version, this scenario
can only be tested manually by:
1. Setting server min_client_version to "999.0.0" via admin DB query
2. Running polyforge doctor
3. Restoring min_client_version to "1.0.0"

SKIP if admin version-control endpoint is not available.

## PASS criteria
Doctor shows [warn] or [FAIL] version when local < min_client_version.
