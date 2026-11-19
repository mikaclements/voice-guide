-- The one workspace and the development product key, if they aren't there yet: of the key, only its
-- SHA-256 and first characters, as voice-guide keeps every key. Run by scripts/lib/seed.sh and the
-- containers' setup (scripts/lib/container.sh), with psql's variables workspace, key_hash and
-- key_display.
INSERT INTO workspaces (id, name) VALUES (:'workspace', 'Default workspace')
ON CONFLICT (id) DO NOTHING;
INSERT INTO product_keys (id, workspace_id, hash, display)
VALUES (uuidv7(), :'workspace', decode(:'key_hash', 'hex'), :'key_display')
ON CONFLICT (hash) DO NOTHING;
