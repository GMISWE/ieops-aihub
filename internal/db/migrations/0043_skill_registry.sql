-- +goose Up

-- aihub#708 Batch 1A: the versioned skill registry — the storage half of the
-- "DB skill bodies replace local authoring bodies for new flows" decision
-- (spec D11). Two tables separate the two lifecycles the design keeps apart
-- (D1):
--
--   skills          the STABLE owner/key identity: an owner and a name, plus
--                   latest_version, the expected-latest CAS token publication
--                   compares against. This row is the only mutable identity
--                   state in the registry, and the only writer of
--                   latest_version is publication under a row lock.
--   skill_versions  the IMMUTABLE versions: integer version, the closed file
--                   bundle, the runtime capability contract, the content
--                   digest. Once inserted, no code path updates bundle,
--                   contract, digest, author_user_id or created_at; the ONLY
--                   mutable column is visibility, which is sharing state and
--                   never version content.
--
-- There is deliberately NO enabled/disabled flag on either table (D1: "There
-- is no enabled flag"). Access is decided per version by the caller's grants,
-- not by a stored on/off bit; adding one would create a second, weaker
-- authorization answer that list/bind could read instead of the grants.
--
-- Sharing model (D1/D3), per VERSION so no sharing ever leaks to a future
-- version:
--   visibility='private' (the default on every insert) → owner and global
--     admin only.
--   visibility='public'  → any authenticated caller. PUBLIC IS NOT ANONYMOUS:
--     the spec's "authenticated registry first" decision means distribution
--     stays behind the auth layer in every route that reads these rows; no
--     unauthenticated route is implied by this value.
--   skill_version_grants → the version is additionally shared with the named
--     project's members and owner. A grant row is per (skill, version,
--     project): sharing v1 with a project says nothing about v2, and v2
--     arrives private.
--
-- Revocation is therefore two operations, both re-checked on every read
-- because accessibility is recomputed from these rows at query time:
--   DELETE the grant row, or flip visibility back to 'private'.
--
-- RETENTION (D1 "referenced versions are retained"): there is no delete path
-- for skill_versions anywhere in the server, so a version pinned by a WI flow
-- (Batch 2, work_items.steps) stays readable by construction. The FK from
-- skill_version_grants to skill_versions is ON DELETE CASCADE for referential
-- hygiene only — no code deletes versions.
--
-- DEPLOY ORDER (DB <-> binary): additive-only (new tables, new indexes, no
-- change to any existing table), so apply BEFORE starting the binary that
-- reads them and ROLLBACK of the binary is safe — the old binary never names
-- these tables. `goose down` drops them and destroys every skill row; that is
-- not part of the rollback procedure.
--
-- The whole Up section is idempotent (CREATE ... IF NOT EXISTS, constraints
-- declared inline in the CREATE) so a test can replay it against a database
-- goose has already migrated — the same replay hazard aihub#444 recorded, and
-- the reason there are no bare ALTER ... ADD CONSTRAINT statements here.

CREATE TABLE IF NOT EXISTS skills (
    id             TEXT PRIMARY KEY,               -- skill_<8 base62>
    owner_user_id  TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    -- the stable key half of the identity: unique per owner. The CHECK mirrors
    -- skillNameRE in internal/domain/skill_registry.go; the parity test
    -- TestSkillRegistryVocabulariesMatchTheMigration fails if either copy
    -- moves (aihub#396 policy: Go validates, the CHECK is the last line of
    -- defence, and a test holds the two together).
    name           TEXT NOT NULL CHECK (name ~ '^[a-z][a-z0-9-]{0,63}$'),
    -- 0 = created, nothing published yet. This is the expected-latest CAS
    -- token: publication reads it FOR UPDATE, refuses when the caller's
    -- expected_latest disagrees, and writes back expected+1 in the same
    -- transaction.
    latest_version INT  NOT NULL DEFAULT 0 CHECK (latest_version >= 0),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (owner_user_id, name)
);

CREATE INDEX IF NOT EXISTS idx_skills_owner ON skills(owner_user_id);

CREATE TABLE IF NOT EXISTS skill_versions (
    skill_id       TEXT NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    version        INT  NOT NULL CHECK (version >= 1),
    -- the closed file bundle: {entry, files[{path,content,encoding?}],
    -- provenance, license}. Validated by internal/skillregistry before
    -- insertion: paths are normalized relative paths (no traversal, no
    -- absolute paths, no drive letters), unknown keys are rejected so a
    -- bundle cannot smuggle a "fetch this at runtime" instruction, and the
    -- set of files is exactly what is stored — nothing outside bundle is
    -- readable through the skill.
    bundle         JSONB NOT NULL,
    -- the runtime capability contract: {capabilities[], input_schema?,
    -- params_schema?, output_schema?, runtime{interactive}}. Schemas are a
    -- supported, explicitly validated JSON Schema subset; anything outside
    -- the subset is refused at publish time (D6: unknown constructs fail
    -- closed rather than pretending a partial validator implements JSON
    -- Schema).
    contract       JSONB NOT NULL,
    -- sha256 over the canonical serialization of bundle+contract,
    -- "sha256:<64 hex>". Computed server-side at publish; a publish may also
    -- carry the same digest and is refused when the two disagree, so batch
    -- 4A's seed/import can be content-verified and idempotent.
    digest         TEXT NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    -- 'private' (default) | 'public'. Sharing state, not version content:
    -- the one mutable column, written by SetSkillVersionVisibility and read
    -- by the accessibility predicate on every query.
    visibility     TEXT NOT NULL DEFAULT 'private' CHECK (visibility IN ('private', 'public')),
    -- who published THIS version (the owner or an admin acting for them).
    author_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (skill_id, version)
);

CREATE TABLE IF NOT EXISTS skill_version_grants (
    skill_id     TEXT NOT NULL,
    version      INT  NOT NULL,
    -- project sharing is a per-version grant, and it requires project rights
    -- at grant time (caller must be the project's owner or a member). ON
    -- DELETE CASCADE: a deleted project takes its grants with it; grants
    -- never outlive the project that is their whole audience.
    project_name TEXT NOT NULL REFERENCES projects(name) ON DELETE CASCADE,
    granted_by   TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (skill_id, version, project_name),
    FOREIGN KEY (skill_id, version)
        REFERENCES skill_versions(skill_id, version) ON DELETE CASCADE
);

-- Reverse lookup for the accessibility predicate's EXISTS arm (a project's
-- members listing skills). The PK's (skill_id, version, project_name) prefix
-- order serves "which versions of skill S are shared"; this serves the other
-- direction.
CREATE INDEX IF NOT EXISTS idx_skill_version_grants_project
    ON skill_version_grants(project_name);

-- +goose Down
DROP TABLE IF EXISTS skill_version_grants;
DROP TABLE IF EXISTS skill_versions;
DROP TABLE IF EXISTS skills;
