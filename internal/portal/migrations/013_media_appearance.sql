CREATE TABLE media_assets(
 id text PRIMARY KEY, owner_id text NOT NULL REFERENCES customers(id),
 scope text NOT NULL CHECK(scope IN ('ticket','announcement','background')),
 mime text NOT NULL CHECK(mime IN ('image/png','image/jpeg')),
 content bytea NOT NULL CHECK(octet_length(content) BETWEEN 1 AND 1048576),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX media_assets_owner ON media_assets(owner_id,created_at);
ALTER TABLE ticket_messages ADD COLUMN images jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(images)='array' AND jsonb_array_length(images)<=6);
ALTER TABLE announcements ADD COLUMN images jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(images)='array' AND jsonb_array_length(images)<=6);
