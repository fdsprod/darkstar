ALTER TABLE work_item_projection ADD COLUMN details TEXT NOT NULL DEFAULT '';
ALTER TABLE work_item_projection ADD COLUMN evidence_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(evidence_json));
ALTER TABLE work_item_projection ADD COLUMN routing_intent_json TEXT NOT NULL DEFAULT '{"mode":"automatic"}' CHECK (json_valid(routing_intent_json));
