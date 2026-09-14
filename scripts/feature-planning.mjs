import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

function sameReference(left, right) {
  return left?.artifactId === right?.artifactId && left?.version === right?.version && left?.sha256 === right?.sha256;
}

function sameIdentity(left, right) {
  return left.projectId === right.projectId && left.featureKey === right.featureKey;
}

function canonical(value) {
  if (Array.isArray(value)) {
    return value.map(canonical);
  }
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.keys(value).sort().map((key) => [key, canonical(value[key])]));
  }
  return value;
}

function equal(left, right) {
  return JSON.stringify(canonical(left)) === JSON.stringify(canonical(right));
}

// Run after JSON Schema validation. Context records must already be loaded by
// exact registry version and verified digest, never by current/latest lookup.
export function validatePlanningSemantics(artifact, context = {}) {
  const issues = [];
  const fail = (code, detail) => issues.push(`${code}: ${detail}`);
  const index = (values, field, label) => {
    const result = new Map();
    for (const value of values) {
      if (result.has(value[field])) {
        fail("DUPLICATE_KEY", `${label} ${value[field]}`);
      }
      result.set(value[field], value);
    }
    return result;
  };
  const references = (values, targets, label) => {
    for (const value of values) {
      if (!targets.has(value)) {
        fail("REFERENCE_MISSING", `${label} ${value}`);
      }
    }
  };
  const bound = (name, reference, type) => {
    const record = context[name];
    if (!record || !sameReference(reference, record.reference) || record.artifact.artifactType !== type || !sameIdentity(artifact, record.artifact)) {
      fail("ARTIFACT_BINDING_MISMATCH", name);
      return undefined;
    }
    return record.artifact;
  };

  const evidence = index(artifact.evidence?.filter((entry) => entry.key) ?? [], "key", "evidence");
  const decisions = index(artifact.decisions ?? [], "key", "decision");
  for (const decision of decisions.values()) {
    references(decision.evidenceKeys, evidence, `decision ${decision.key} evidence`);
    references(decision.state.evidenceKeys ?? [], evidence, `decision ${decision.key} resolution evidence`);
  }

  if (artifact.artifactType === "feature_brief") {
    index(artifact.requirements, "key", "requirement");
    index(artifact.repositories, "repositoryId", "repository");
    for (const requirement of artifact.requirements) {
      references(requirement.evidenceKeys, evidence, `requirement ${requirement.key} evidence`);
    }
  }

  if (artifact.artifactType === "story_backlog") {
    const brief = bound("brief", artifact.brief, "feature_brief");
    const stories = index(artifact.stories, "key", "story");
    const active = new Map(artifact.stories.filter((story) => story.lifecycle.kind === "active").map((story) => [story.key, story]));
    const requirements = new Map((brief?.requirements ?? []).map((item) => [item.key, item]));
    const repositories = new Map((brief?.repositories ?? []).map((item) => [item.repositoryId, item]));
    for (const story of artifact.stories) {
      // Tombstones retain the last content, whose references belonged to its
      // historical brief. They are not executable or republishable drafts.
      if (story.lifecycle.kind === "active") {
        references(story.requirementKeys, requirements, `${story.key} requirement`);
        references(story.evidenceKeys, evidence, `${story.key} evidence`);
        references(story.decisionKeys, decisions, `${story.key} decision`);
        references(story.dependencies, active, `${story.key} dependency`);
        if (story.repositoryImpact.kind === "repositories") {
          references(story.repositoryImpact.repositoryIds, repositories, `${story.key} repository`);
        }
      }
      if (story.lifecycle.kind === "split") {
        references(story.lifecycle.replacementKeys, stories, `${story.key} replacement`);
        if (story.lifecycle.replacementKeys.includes(story.key)) {
          fail("SPLIT_INVALID", `${story.key} replaces itself`);
        }
      }
    }
    const visitGraph = (entries, children, label) => {
      const visited = new Set();
      const visiting = new Set();
      const visit = (key) => {
        if (visiting.has(key)) {
          fail("CYCLE", `${label} ${key}`);
          return;
        }
        if (visited.has(key) || !entries.has(key)) {
          return;
        }
        visiting.add(key);
        for (const child of children(entries.get(key))) {
          visit(child);
        }
        visiting.delete(key);
        visited.add(key);
      };
      for (const key of entries.keys()) {
        visit(key);
      }
    };
    visitGraph(active, (story) => story.dependencies, "dependency");
    visitGraph(stories, (story) => story.lifecycle.replacementKeys ?? [], "split");
  }

  if (["story_links", "handoff_observation"].includes(artifact.artifactType)) {
    const backlog = bound("backlog", artifact.backlog, "story_backlog");
    const stories = new Map((backlog?.stories ?? []).map((story) => [story.key, story]));
    if (artifact.artifactType === "story_links") {
      index(artifact.links, "storyKey", "linked story");
      index(artifact.links, "ticketId", "native ticket");
      references(artifact.links.map((link) => link.storyKey), stories, "linked story");
    } else {
      references(artifact.storyKeys, stories, "handoff story");
      if (artifact.milestone.kind === "plan_approved" && !sameReference(artifact.milestone.backlog, artifact.backlog)) {
        fail("ARTIFACT_BINDING_MISMATCH", "approval backlog");
      }
      if (artifact.milestone.kind === "plan_approved" && artifact.authority.kind !== "daemon") {
        fail("HANDOFF_AUTHORITY_INVALID", "plan approval must cite a recorded daemon approval decision");
      }
      if (artifact.milestone.repositoryId) {
        const brief = bound("brief", backlog?.brief, "feature_brief");
        const repositories = new Map((brief?.repositories ?? []).map((entry) => [entry.repositoryId, entry]));
        references([artifact.milestone.repositoryId], repositories, "handoff repository");
      }
    }
  }

  if (artifact.lineage?.kind === "initial") {
    if (context.previous || artifact.stories?.some((story) => story.lifecycle.kind !== "active")) {
      fail("LINEAGE_REQUIRED", "initial artifacts cannot replace a revision or invent tombstones");
    }
  } else if (artifact.lineage?.kind === "revision") {
    const previous = bound("previous", artifact.lineage.previous, artifact.artifactType);
    if (previous) {
      const oldStories = new Map((previous.stories ?? []).map((story) => [story.key, story]));
      const nextStories = new Map((artifact.stories ?? []).map((story) => [story.key, story]));
      const replacements = new Set();
      for (const old of oldStories.values()) {
        const next = nextStories.get(old.key);
        if (!next) {
          fail("KEY_REMOVED", `story ${old.key}; retain a tombstone`);
          continue;
        }
        if (old.lifecycle.kind !== "active" && !equal(old, next)) {
          fail("TOMBSTONE_CHANGED", old.key);
        }
        if (old.lifecycle.kind === "active" && next.lifecycle.kind !== "active") {
          if (!equal({...old, lifecycle: next.lifecycle}, next)) {
            fail("RETIREMENT_CONTENT_CHANGED", old.key);
          }
          for (const key of next.lifecycle.replacementKeys ?? []) {
            if (oldStories.has(key) || replacements.has(key) || nextStories.get(key)?.lifecycle.kind !== "active") {
              fail("SPLIT_INVALID", `${old.key} replacement ${key} must be new and active with one parent`);
            }
            replacements.add(key);
          }
        }
      }
      for (const story of nextStories.values()) {
        if (!oldStories.has(story.key) && story.lifecycle.kind !== "active") {
          fail("LINEAGE_REQUIRED", `new story ${story.key} cannot start retired`);
        }
      }
    }
  } else if (artifact.lineage?.kind === "migration") {
    const source = context.migrationSource;
    if (!source || !sameReference(source.reference, artifact.lineage.source) || source.artifact.schemaVersion !== 1) {
      fail("MIGRATION_SOURCE_INVALID", "exact legacy source required");
    } else {
      const mappings = artifact.lineage.keyMappings;
      index(mappings, "sourceId", "migration source");
      index(mappings, "key", "migration target");
      if (artifact.artifactType === "story_backlog" && source.artifact.artifactType === "delivery_plan") {
        const oldStories = index(source.artifact.stories, "id", "legacy story");
        const newStories = new Map(artifact.stories.map((story) => [story.key, story]));
        references(mappings.map((item) => item.sourceId), oldStories, "migration source");
        references(mappings.map((item) => item.key), newStories, "migration target");
        references([...oldStories.keys()], new Map(mappings.map((item) => [item.sourceId, item])), "unmapped legacy story");
      } else if (artifact.artifactType !== "feature_brief" || source.artifact.artifactType !== "product_brief" || mappings.length !== 0) {
        fail("MIGRATION_SOURCE_INVALID", "supported migrations: product_brief to feature_brief; delivery_plan to story_backlog");
      }
    }
    if (context.previous || artifact.stories?.some((story) => story.lifecycle.kind !== "active")) {
      fail("LINEAGE_REQUIRED", "migration creates a new artifact with active stories");
    }
  }
  return issues;
}

// Escape all source text: projections cannot introduce HTML, links, or headings.
function text(value) {
  return String(value).replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;").replace(/[\\`*_{}\[\]()#+.!|~-]/g, "\\$&").replace(/\r?\n/g, "<br>");
}

function fields(value, depth = 0) {
  const lines = [];
  for (const [key, entry] of Object.entries(value)) {
    const prefix = "  ".repeat(depth);
    if (entry && typeof entry === "object") {
      lines.push(`${prefix}- **${label(key)}**${Object.keys(entry).length === 0 ? ": None" : ""}`);
      if (Array.isArray(entry)) {
        for (const item of entry) {
          if (item && typeof item === "object") {
            lines.push(`${prefix}  - Item`);
            lines.push(...fields(item, depth + 2));
          } else {
            lines.push(`${prefix}  - ${text(item)}`);
          }
        }
      } else {
        lines.push(...fields(entry, depth + 1));
      }
    } else {
      lines.push(`${prefix}- **${label(key)}:** ${text(entry)}`);
    }
  }
  return lines;
}

function label(key) {
  const words = key.replace(/([a-z])([A-Z])/g, "$1 $2").replaceAll("_", " ");
  return text(words.charAt(0).toUpperCase() + words.slice(1));
}

export function planningMarkdown(artifact) {
  const title = artifact.artifactType === "feature_brief" ? artifact.title : `${artifact.featureKey} ${artifact.artifactType.replaceAll("_", " ")}`;
  const lines = [`# ${text(title)}`, "", "> Derived planning projection. Approval and business completion are separate records.", ""];
  const metadata = {};
  for (const [key, value] of Object.entries(artifact)) {
    if (["artifactType", "schemaVersion", "projectId", "featureKey", "lineage", "brief", "backlog"].includes(key)) {
      metadata[key] = value;
      continue;
    }
    if (key === "title") {
      continue;
    }
    lines.push(`## ${key === "decisions" ? "Decisions" : label(key)}`, "");
    if (key === "stories") {
      for (const story of value) {
        lines.push(`### ${text(story.key)}: ${text(story.title)}`, "", `**Lifecycle:** ${text(story.lifecycle.kind)}`, "", text(story.outcome), "");
        for (const field of ["acceptanceCriteria", "scope", "requirementKeys", "repositoryImpact", "dependencies", "evidenceKeys", "decisionKeys", "lifecycle"]) {
          lines.push(...fields({[field]: story[field]}), "");
        }
      }
    } else if (typeof value === "string") {
      lines.push(text(value), "");
    } else if (Array.isArray(value)) {
      if (!value.length) {
        lines.push("None.", "");
      }
      for (const item of value) {
        lines.push(...(typeof item === "object" ? fields(item) : [`- ${text(item)}`]), "");
      }
    } else {
      lines.push(...fields(value), "");
    }
  }
  lines.push("## Provenance", "", ...fields(metadata), "");
  return lines.join("\n");
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const [command, artifactPath, contextPath] = process.argv.slice(2);
  if (command === "validate" && artifactPath) {
    const artifact = JSON.parse(readFileSync(artifactPath, "utf8"));
    const context = contextPath ? JSON.parse(readFileSync(contextPath, "utf8")) : {};
    const issues = validatePlanningSemantics(artifact, context);
    console.log(issues.length ? issues.join("\n") : "Planning semantic constraints passed (JSON Schema validation is a separate prerequisite).");
    process.exitCode = issues.length ? 1 : 0;
  } else if (command === "markdown" && artifactPath) {
    process.stdout.write(planningMarkdown(JSON.parse(readFileSync(artifactPath, "utf8"))));
  } else {
    console.error("Usage: node scripts/feature-planning.mjs <validate|markdown> <artifact.json> [context.json]");
    process.exitCode = 2;
  }
}
