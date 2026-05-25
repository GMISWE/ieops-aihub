-- Seed test users and API keys for integration tests.
-- Run after migrations: psql $DATABASE_URL -f seed_test_data.sql

-- Admin user (role=admin) — used for scenario config updates and admin ops
INSERT INTO users (id, email, display_name, user_type, role, api_keys)
VALUES (
    'u_test_admin_001',
    'test-admin@integration.test',
    'Test Admin',
    'human',
    'admin',
    '[{
        "id":       "k_test_admin_key",
        "key_hash": "70231588a2faa0d058a8137103afd4d493885ac717532029a0c9e65aa39366bd",
        "name":     "test-admin-key"
    }]'
) ON CONFLICT (id) DO NOTHING;

-- Writer user (role=writer) — project access granted via projects.members
INSERT INTO users (id, email, display_name, user_type, role, api_keys)
VALUES (
    'u_test_writer_001',
    'test-writer@integration.test',
    'Test Writer',
    'human',
    'writer',
    '[{
        "id":       "k_test_writer_key",
        "key_hash": "386d4f158dde9caf09caf6632b32355c5c274374afa036eace10454a5827b9a0",
        "name":     "test-writer-key"
    }]'
) ON CONFLICT (id) DO NOTHING;

-- Maintainer user (role=writer) — project access granted via projects.members
INSERT INTO users (id, email, display_name, user_type, role, api_keys)
VALUES (
    'u_test_maintainer_001',
    'test-maintainer@integration.test',
    'Test Maintainer',
    'human',
    'writer',
    '[{
        "id":       "k_test_maint_key",
        "key_hash": "4cc3fc7f760c74fc1d1fb6b8e9f368031aabc6d3270ed61f9395a62ffcbc2c23",
        "name":     "test-maintainer-key"
    }]'
) ON CONFLICT (id) DO NOTHING;
