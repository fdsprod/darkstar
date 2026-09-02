import assert from "node:assert/strict";
import test from "node:test";

import { lintPlanningTemplates } from "../scripts/planning-artifacts.mjs";

test("planning artifact templates are complete and schema-driven", () => {
  const result = lintPlanningTemplates();
  assert.deepEqual(result.issues, []);
  assert.deepEqual(result.artifactTypes, [
    "delivery_plan",
    "experience_design",
    "implementation_plan",
    "poc_findings",
    "product_brief",
    "product_requirements",
    "story_design",
    "story_research",
    "technical_design",
    "technical_research",
  ]);
  assert.equal(result.files.length, result.artifactTypes.length);
});
