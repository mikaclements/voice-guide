-- The one workspace and the development product key, if they aren't there yet: of the key, only its
-- SHA-256 and first characters, as voice-guide keeps every key. Run by scripts/lib/seed.sh and the
-- containers' setup (scripts/lib/container.sh), with psql's variables workspace, key_hash and
-- key_display. The development key holds every permission, so a start with only Docker reaches the
-- agent routes as well as the session routes; a key seeded before it held permissions gains them.
INSERT INTO workspaces (id, name) VALUES (:'workspace', 'Default workspace')
ON CONFLICT (id) DO NOTHING;
INSERT INTO product_keys (id, workspace_id, hash, display, permissions)
VALUES (uuidv7(), :'workspace', decode(:'key_hash', 'hex'), :'key_display', '{run_sessions,publish_agents}')
ON CONFLICT (hash) DO UPDATE SET permissions = excluded.permissions;
