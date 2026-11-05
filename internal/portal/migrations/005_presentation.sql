ALTER TABLE plans ADD COLUMN display_features jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(display_features)='array' AND jsonb_array_length(display_features)<=12);
ALTER TABLE announcements ADD COLUMN pinned boolean NOT NULL DEFAULT false;
ALTER TABLE announcements ADD COLUMN pin_order integer NOT NULL DEFAULT 0;
CREATE INDEX announcements_display ON announcements(enabled,pinned DESC,pin_order,created_at DESC);
UPDATE settings SET public=jsonb_set(public,'{traffic_unit}','"GiB"'),version=version+1;
