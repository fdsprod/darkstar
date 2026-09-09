ALTER TABLE work_item_projection ADD COLUMN deletion TEXT NOT NULL DEFAULT '' CHECK (deletion IN ('', 'deleting', 'deleted'));
