import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { planningMarkdown, validatePlanningSemantics } from "../scripts/feature-planning.mjs";

const load = (name) => JSON.parse(readFileSync(new URL(`../examples/feature-planning/${name}.json`, import.meta.url), "utf8"));
const briefReference = load("backlog").brief;
const backlogReference = load("links").backlog;
const context = () => ({brief: {reference: briefReference, artifact: load("brief")}, backlog: {reference: backlogReference, artifact: load("backlog")}});
const validate = (value, bindings = context()) => validatePlanningSemantics(value, bindings);

test("closed planning examples have valid semantic references", () => {
  for (const name of ["brief", "backlog", "links", "handoff"]) {
    assert.deepEqual(validate(load(name)), []);
  }
});

test("shipped Markdown projections stay fresh for every record", () => {
  for (const name of ["brief", "backlog", "links", "handoff"]) {
    const rendered = readFileSync(new URL(`../templates/feature-planning/${name}.md`, import.meta.url), "utf8");
    assert.equal(rendered, planningMarkdown(load(name)));
  }
});

test("every keyed catalog rejects duplicates even with different contents", () => {
  for (const [name, field, changed] of [["brief", "requirements", "outcome"], ["brief", "repositories", "membershipRevision"], ["brief", "evidence", "finding"], ["brief", "decisions", "question"], ["backlog", "stories", "title"], ["backlog", "evidence", "finding"], ["backlog", "decisions", "question"], ["links", "links", "url"]]) {
    const value = load(name);
    value[field].push({...value[field][0], [changed]: changed === "membershipRevision" ? 2 : "different"});
    assert.match(validate(value).join("\n"), /DUPLICATE_KEY/, `${name}.${field}`);
  }
});

test("story references resolve only against the exact brief and local catalogs", () => {
  for (const [field, value] of [["requirementKeys", ["missing"]], ["dependencies", ["missing"]], ["evidenceKeys", ["missing"]], ["decisionKeys", ["missing"]], ["repositoryImpact", {kind: "repositories", repositoryIds: ["missing"]}]]) {
    const backlog = load("backlog");
    backlog.stories[0][field] = value;
    assert.match(validate(backlog).join("\n"), /REFERENCE_MISSING/, field);
  }
  for (const field of ["artifactId", "version", "sha256"]) {
    const backlog = load("backlog");
    backlog.brief[field] = field === "version" ? 2 : "different";
    assert.match(validate(backlog).join("\n"), /ARTIFACT_BINDING_MISMATCH/);
  }
  for (const field of ["featureKey", "projectId"]) {
    const bindings = context();
    bindings.brief.artifact[field] = "other";
    assert.match(validate(load("backlog"), bindings).join("\n"), /ARTIFACT_BINDING_MISMATCH/);
  }
});

test("requirement and decision evidence cannot dangle", () => {
  for (const [name, field] of [["brief", "requirements"], ["brief", "decisions"], ["backlog", "decisions"]]) {
    const value = load(name);
    value[field][0].evidenceKeys = ["missing"];
    assert.match(validate(value).join("\n"), /REFERENCE_MISSING/);
  }
});

test("dependency cycles and self dependencies are rejected", () => {
  const backlog = load("backlog");
  backlog.stories[0].dependencies = ["STORY-2"];
  assert.match(validate(backlog).join("\n"), /CYCLE/);
  backlog.stories[0].dependencies = ["STORY-1"];
  assert.match(validate(backlog).join("\n"), /CYCLE/);
});

test("unknown, explicit none, and selected repository impacts remain distinct", () => {
  const backlog = load("backlog");
  assert.equal(backlog.stories[0].repositoryImpact.kind, "none");
  assert.equal(backlog.stories[1].repositoryImpact.kind, "unknown");
  backlog.stories[1].repositoryImpact = {kind: "repositories", repositoryIds: ["repo-runtime"]};
  assert.deepEqual(validate(backlog), []);
  const bindings = context();
  bindings.brief.artifact.repositories = [];
  backlog.stories[1].repositoryImpact = {kind: "none", rationale: "No code change"};
  assert.deepEqual(validate(backlog, bindings), []);
});

function revision() {
  const previous = load("backlog");
  const next = structuredClone(previous);
  next.lineage = {kind: "revision", previous: structuredClone(backlogReference)};
  return {next, bindings: {...context(), previous: {reference: backlogReference, artifact: previous}}};
}

test("renaming and reordering stories preserves stable identity", () => {
  const {next, bindings} = revision();
  next.stories.reverse();
  next.stories[0].title = "Revised description";
  assert.deepEqual(validate(next, bindings), []);
  assert.deepEqual(next.stories.map((story) => story.key), ["STORY-2", "STORY-1"]);
});

test("revision lineage cannot cross artifact type, feature, project, or version", () => {
  for (const field of ["artifactType", "featureKey", "projectId"]) {
    const {next, bindings} = revision();
    bindings.previous.artifact[field] = "other";
    assert.match(validate(next, bindings).join("\n"), /ARTIFACT_BINDING_MISMATCH/);
  }
  const {next, bindings} = revision();
  next.lineage.previous.version++;
  assert.match(validate(next, bindings).join("\n"), /ARTIFACT_BINDING_MISMATCH/);
});

test("removal needs a tombstone; dependent active stories must be revised", () => {
  const {next, bindings} = revision();
  next.stories[0].lifecycle = {kind: "retired", reason: "Out of scope"};
  assert.match(validate(next, bindings).join("\n"), /REFERENCE_MISSING/);
  next.stories[1].dependencies = [];
  assert.deepEqual(validate(next, bindings), []);
  next.stories.shift();
  assert.match(validate(next, bindings).join("\n"), /KEY_REMOVED/);
});

test("retirement preserves content and old tombstones cannot resurrect or change", () => {
  const {next, bindings} = revision();
  next.stories[1].lifecycle = {kind: "retired", reason: "No longer needed"};
  next.stories[1].title = "Lost old title";
  assert.match(validate(next, bindings).join("\n"), /RETIREMENT_CONTENT_CHANGED/);
  bindings.previous.artifact.stories[1].lifecycle = {kind: "retired", reason: "No longer needed"};
  next.stories[1] = structuredClone(bindings.previous.artifact.stories[1]);
  assert.deepEqual(validate(next, bindings), []);
  next.stories[1].lifecycle = {kind: "active"};
  assert.match(validate(next, bindings).join("\n"), /TOMBSTONE_CHANGED/);
});

test("split retains parent and creates new active child identities", () => {
  const {next, bindings} = revision();
  next.stories[1].lifecycle = {kind: "split", reason: "Separate investigation", replacementKeys: ["STORY-3", "STORY-4"]};
  for (const key of ["STORY-3", "STORY-4"]) {
    next.stories.push({...structuredClone(bindings.previous.artifact.stories[1]), key});
  }
  assert.deepEqual(validate(next, bindings), []);
  next.stories[1].lifecycle.replacementKeys = ["STORY-1", "STORY-4"];
  assert.match(validate(next, bindings).join("\n"), /SPLIT_INVALID/);
  next.stories[1].lifecycle.replacementKeys = ["missing", "STORY-4"];
  assert.match(validate(next, bindings).join("\n"), /REFERENCE_MISSING/);
});

test("initial snapshots cannot invent retired lineage or reset prior identity", () => {
  const backlog = load("backlog");
  backlog.stories[1].lifecycle = {kind: "retired", reason: "Unknown history"};
  assert.match(validate(backlog).join("\n"), /LINEAGE_REQUIRED/);
  const {bindings} = revision();
  assert.match(validate(load("backlog"), bindings).join("\n"), /LINEAGE_REQUIRED/);
});

test("catalog edits preserve old revisions and cannot orphan current references", () => {
  const brief = load("brief");
  const next = structuredClone(brief);
  next.lineage = {kind: "revision", previous: briefReference};
  next.evidence = [];
  assert.match(validate(next, {previous: {reference: briefReference, artifact: brief}}).join("\n"), /REFERENCE_MISSING/);
  next.requirements[0].evidenceKeys = [];
  next.decisions = [];
  assert.deepEqual(validate(next, {previous: {reference: briefReference, artifact: brief}}), []);
  assert.equal(brief.decisions.length, 1);
});

test("decisions resolve with evidence without leaving a stale blocking flag", () => {
  const brief = load("brief");
  brief.decisions[0].state = {kind: "resolved", resolution: "Use Linear", evidenceKeys: ["E-1"]};
  assert.deepEqual(validate(brief), []);
  brief.decisions[0].state.evidenceKeys = ["unknown"];
  assert.match(validate(brief).join("\n"), /REFERENCE_MISSING/);
});

test("legacy migration demands exact source and total unambiguous story mapping", () => {
  const backlog = load("backlog");
  backlog.lineage = {kind: "migration", source: backlogReference, sourceSchema: "planning-artifact-v1alpha1", keyMappings: [{sourceId: "old-1", key: "STORY-1"}, {sourceId: "old-2", key: "STORY-2"}], notes: ["Required context preserved as evidence"]};
  const bindings = {...context(), migrationSource: {reference: backlogReference, artifact: {artifactType: "delivery_plan", schemaVersion: 1, stories: [{id: "old-1"}, {id: "old-2"}]}}};
  assert.deepEqual(validate(backlog, bindings), []);
  backlog.lineage.keyMappings.pop();
  assert.match(validate(backlog, bindings).join("\n"), /REFERENCE_MISSING/);
  backlog.lineage.keyMappings.push({sourceId: "old-1", key: "STORY-2"});
  assert.match(validate(backlog, bindings).join("\n"), /DUPLICATE_KEY/);
});

test("external identity is separate and duplicate/dangling native links fail", () => {
  const links = load("links");
  links.destinationBindingRevision = 9;
  assert.deepEqual(validate(links), []);
  links.links.push({...links.links[0], storyKey: "STORY-2"});
  assert.match(validate(links).join("\n"), /DUPLICATE_KEY: native ticket/);
  links.links = [{...links.links[0], storyKey: "unknown"}];
  assert.match(validate(links).join("\n"), /REFERENCE_MISSING/);
});

test("handoff binds exact backlog and recorded authority without completion flags", () => {
  const handoff = load("handoff");
  handoff.milestone.backlog.version = 2;
  assert.match(validate(handoff).join("\n"), /ARTIFACT_BINDING_MISMATCH/);
  handoff.milestone.backlog.version = 1;
  handoff.authority = {kind: "human", actorId: "actor", decisionId: "decision"};
  assert.match(validate(handoff).join("\n"), /HANDOFF_AUTHORITY_INVALID/);
  handoff.milestone = {kind: "tests_passed", repositoryId: "missing", commit: "abc", validationProfile: "unit"};
  assert.match(validate(handoff).join("\n"), /REFERENCE_MISSING/);
});

test("Markdown projection is deterministic, complete, escaped, and nonmutating", () => {
  const {next, bindings} = revision();
  next.stories[1].lifecycle = {kind: "retired", reason: "No longer relevant"};
  next.stories[0].title = "<script>alert(1)</script> [link](https://bad.invalid)";
  const before = structuredClone(next);
  const markdown = planningMarkdown(next);
  assert.equal(markdown, planningMarkdown(next));
  assert.deepEqual(next, before);
  assert.match(markdown, /&lt;script&gt;/);
  assert.doesNotMatch(markdown, /<script>|\[link\]\(https/);
  for (const phrase of ["No longer relevant", "Repository Impact", "Requirement Keys", "Evidence", "Decisions", "Sha256", "Lineage"]) {
    assert.ok(markdown.includes(phrase), phrase);
  }
  assert.deepEqual(validate(next, bindings), []);
});
