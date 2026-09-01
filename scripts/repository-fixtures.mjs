#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import {
  existsSync,
  mkdirSync,
  readFileSync,
  realpathSync,
  writeFileSync,
} from "node:fs";
import { dirname, isAbsolute, relative, resolve, sep } from "node:path";
import { pathToFileURL } from "node:url";

const ID_PATTERN = /^[a-z][a-z0-9_-]{0,79}$/;
const BRANCH_PATTERN = /^(?![-/.])(?!.*(?:\.\.|\/\/|@\{|\\|\s|[~^:?*\[]))(?!.*[/.]$)[A-Za-z0-9._/-]+$/;

export class RepositoryFixtureError extends Error {
  constructor(code, message, location = "") {
    super(message);
    this.code = code;
    this.location = location;
  }

  toJSON() {
    return { code: this.code, message: this.message, ...(this.location ? { location: this.location } : {}) };
  }
}

function fail(code, message, location = "") {
  throw new RepositoryFixtureError(code, message, location);
}

function lines(value) {
  const withoutTrailingNewline = value.replace(/[\r\n]+$/, "");
  return withoutTrailingNewline ? withoutTrailingNewline.split(/\r?\n/) : [];
}

function assertRelativePath(value, location) {
  if (typeof value !== "string" || !value || isAbsolute(value)) {
    fail("REPOSITORY_FIXTURE_PATH_INVALID", "fixture paths must be nonempty relative paths", location);
  }
  const normalized = value.replaceAll("\\", "/");
  if (normalized.split("/").some((part) => !part || part === "." || part === "..")) {
    fail("REPOSITORY_FIXTURE_PATH_INVALID", `unsafe fixture path '${value}'`, location);
  }
}

function assertBranch(value, location) {
  if (typeof value !== "string" || !BRANCH_PATTERN.test(value) || value.endsWith(".lock")) {
    fail("REPOSITORY_FIXTURE_BRANCH_INVALID", `invalid fixture branch '${value}'`, location);
  }
}

export function validateManifest(document) {
  const errors = [];
  const capture = (fn) => {
    try { fn(); }
    catch (cause) {
      errors.push(cause instanceof RepositoryFixtureError
        ? cause
        : new RepositoryFixtureError("REPOSITORY_FIXTURE_INVALID", cause.message));
    }
  };

  if (!document || document.schemaVersion !== 1 || !document.git || !Array.isArray(document.fixtures)) {
    return [new RepositoryFixtureError("REPOSITORY_FIXTURE_INVALID", "schemaVersion 1, git settings, and fixtures are required")];
  }
  if (document.fixtures.length === 0) {
    errors.push(new RepositoryFixtureError("REPOSITORY_FIXTURE_INVALID", "fixtures must not be empty", "/fixtures"));
  }
  capture(() => assertBranch(document.git.initialBranch, "/git/initialBranch"));
  for (const field of ["authorName", "authorEmail", "timestamp", "commitMessage"]) {
    if (typeof document.git[field] !== "string" || !document.git[field]) {
      errors.push(new RepositoryFixtureError("REPOSITORY_FIXTURE_INVALID", `${field} must be a nonempty string`, `/git/${field}`));
    }
  }
  if (Number.isNaN(Date.parse(document.git.timestamp))) {
    errors.push(new RepositoryFixtureError("REPOSITORY_FIXTURE_INVALID", "timestamp must be an ISO-8601 timestamp", "/git/timestamp"));
  }
  if (!document.baseFiles || typeof document.baseFiles !== "object" || Array.isArray(document.baseFiles)) {
    errors.push(new RepositoryFixtureError("REPOSITORY_FIXTURE_INVALID", "baseFiles must be an object", "/baseFiles"));
  } else {
    for (const [path, content] of Object.entries(document.baseFiles)) {
      capture(() => assertRelativePath(path, `/baseFiles/${path}`));
      if (typeof content !== "string") errors.push(new RepositoryFixtureError("REPOSITORY_FIXTURE_INVALID", "file content must be a string", `/baseFiles/${path}`));
    }
  }

  const ids = new Set();
  document.fixtures.forEach((fixture, index) => {
    const location = `/fixtures/${index}`;
    if (!fixture || typeof fixture !== "object" || Array.isArray(fixture)) {
      errors.push(new RepositoryFixtureError("REPOSITORY_FIXTURE_INVALID", "fixture must be an object", location));
      return;
    }
    if (typeof fixture.id !== "string" || !ID_PATTERN.test(fixture.id)) {
      errors.push(new RepositoryFixtureError("REPOSITORY_FIXTURE_INVALID", "fixture needs a stable snake-case id", `${location}/id`));
    } else if (ids.has(fixture.id)) {
      errors.push(new RepositoryFixtureError("REPOSITORY_FIXTURE_INVALID", `duplicate fixture id '${fixture.id}'`, `${location}/id`));
    } else ids.add(fixture.id);
    if (!Array.isArray(fixture.covers) || fixture.covers.length === 0) {
      errors.push(new RepositoryFixtureError("REPOSITORY_FIXTURE_INVALID", "covers must be a nonempty array", `${location}/covers`));
    }
    for (const [branchIndex, branch] of (fixture.branches ?? []).entries()) {
      capture(() => assertBranch(branch, `${location}/branches/${branchIndex}`));
    }
    for (const [changeIndex, change] of (fixture.changes ?? []).entries()) {
      capture(() => assertRelativePath(change?.path, `${location}/changes/${changeIndex}/path`));
      if (typeof change?.content !== "string") errors.push(new RepositoryFixtureError("REPOSITORY_FIXTURE_INVALID", "change content must be a string", `${location}/changes/${changeIndex}/content`));
    }
    for (const [worktreeIndex, worktree] of (fixture.worktrees ?? []).entries()) {
      if (!worktree || typeof worktree !== "object") {
        errors.push(new RepositoryFixtureError("REPOSITORY_FIXTURE_INVALID", "worktree must be an object", `${location}/worktrees/${worktreeIndex}`));
        continue;
      }
      capture(() => assertRelativePath(worktree.name, `${location}/worktrees/${worktreeIndex}/name`));
      capture(() => assertBranch(worktree.branch, `${location}/worktrees/${worktreeIndex}/branch`));
      capture(() => assertBranch(worktree.startPoint, `${location}/worktrees/${worktreeIndex}/startPoint`));
    }
    if (!fixture.expected || typeof fixture.expected !== "object") {
      errors.push(new RepositoryFixtureError("REPOSITORY_FIXTURE_INVALID", "expected observation is required", `${location}/expected`));
    }
  });
  return errors;
}

function gitEnvironment(settings) {
  return {
    ...process.env,
    GIT_CONFIG_NOSYSTEM: "1",
    GIT_CONFIG_GLOBAL: process.platform === "win32" ? "NUL" : "/dev/null",
    GIT_AUTHOR_NAME: settings.authorName,
    GIT_AUTHOR_EMAIL: settings.authorEmail,
    GIT_AUTHOR_DATE: settings.timestamp,
    GIT_COMMITTER_NAME: settings.authorName,
    GIT_COMMITTER_EMAIL: settings.authorEmail,
    GIT_COMMITTER_DATE: settings.timestamp,
  };
}

function git(cwd, args, settings) {
  const result = spawnSync("git", args, {
    cwd,
    encoding: "utf8",
    env: gitEnvironment(settings),
    windowsHide: true,
  });
  if (result.error) fail("REPOSITORY_FIXTURE_GIT_FAILED", result.error.message);
  if (result.status !== 0) {
    const detail = (result.stderr || result.stdout || "unknown Git error").trim();
    fail("REPOSITORY_FIXTURE_GIT_FAILED", `git ${args[0]} failed: ${detail}`);
  }
  return result.stdout;
}

function writeFixtureFile(root, path, content) {
  assertRelativePath(path, path);
  const destination = resolve(root, path);
  const relativeDestination = relative(root, destination);
  if (relativeDestination.startsWith(`..${sep}`) || relativeDestination === "..") {
    fail("REPOSITORY_FIXTURE_PATH_INVALID", `fixture path escapes repository: '${path}'`, path);
  }
  mkdirSync(dirname(destination), { recursive: true });
  writeFileSync(destination, content, { encoding: "utf8", flag: "w" });
}

function parseWorktrees(output) {
  const records = [];
  let current = null;
  for (const line of output.split(/\r?\n/)) {
    if (line.startsWith("worktree ")) {
      current = { path: line.slice("worktree ".length), head: null, branch: null };
      records.push(current);
    } else if (current && line.startsWith("HEAD ")) current.head = line.slice("HEAD ".length);
    else if (current && line.startsWith("branch refs/heads/")) current.branch = line.slice("branch refs/heads/".length);
    else if (current && line === "detached") current.branch = "(detached)";
  }
  return records;
}

export function snapshotRepositoryFixture(scenarioRoot, settings) {
  const repository = resolve(scenarioRoot, "repository");
  const head = git(repository, ["rev-parse", "HEAD"], settings).trim();
  const headBranch = git(repository, ["branch", "--show-current"], settings).trim();
  const status = lines(git(repository, ["status", "--porcelain=v1", "--untracked-files=all"], settings));
  const branches = lines(git(repository, ["for-each-ref", "--format=%(refname:short)", "refs/heads"], settings)).sort();
  const worktrees = parseWorktrees(git(repository, ["worktree", "list", "--porcelain"], settings)).map((worktree) => {
    const canonicalPath = realpathSync.native(worktree.path);
    const normalizedPath = relative(realpathSync.native(scenarioRoot), canonicalPath).replaceAll("\\", "/");
    return {
      path: normalizedPath,
      branch: worktree.branch,
      head: worktree.head,
      status: lines(git(canonicalPath, ["status", "--porcelain=v1", "--untracked-files=all"], settings)),
    };
  }).sort((a, b) => a.path.localeCompare(b.path));
  return { head, headBranch, status, branches, worktrees };
}

function expectedObservation(snapshot) {
  return {
    headBranch: snapshot.headBranch,
    status: snapshot.status,
    branches: snapshot.branches,
    worktreeBranches: snapshot.worktrees.map((item) => item.branch).sort(),
  };
}

export function materializeRepositoryFixtures(document, destination) {
  const validation = validateManifest(document);
  if (validation.length) throw validation[0];
  const outputRoot = resolve(destination);
  if (existsSync(outputRoot)) {
    fail("REPOSITORY_FIXTURE_DESTINATION_EXISTS", `destination already exists: ${outputRoot}`);
  }
  mkdirSync(outputRoot, { recursive: false });

  const results = [];
  for (const fixture of document.fixtures) {
    const scenarioRoot = resolve(outputRoot, fixture.id);
    const repository = resolve(scenarioRoot, "repository");
    mkdirSync(repository, { recursive: true });
    for (const [path, content] of Object.entries(document.baseFiles)) writeFixtureFile(repository, path, content);

    git(repository, ["init", `--initial-branch=${document.git.initialBranch}`, "--template="], document.git);
    git(repository, ["config", "core.autocrlf", "false"], document.git);
    git(repository, ["config", "core.filemode", "false"], document.git);
    git(repository, ["config", "commit.gpgsign", "false"], document.git);
    git(repository, ["add", "--all"], document.git);
    git(repository, ["commit", "--no-verify", "-m", document.git.commitMessage], document.git);

    for (const branch of fixture.branches ?? []) git(repository, ["branch", branch, document.git.initialBranch], document.git);
    for (const worktree of fixture.worktrees ?? []) {
      const worktreePath = resolve(scenarioRoot, "worktrees", worktree.name);
      mkdirSync(dirname(worktreePath), { recursive: true });
      git(repository, ["worktree", "add", "-b", worktree.branch, worktreePath, worktree.startPoint], document.git);
    }
    for (const change of fixture.changes ?? []) writeFixtureFile(repository, change.path, change.content);

    const snapshot = snapshotRepositoryFixture(scenarioRoot, document.git);
    const actual = expectedObservation(snapshot);
    results.push({
      id: fixture.id,
      covers: fixture.covers,
      snapshot,
      pass: JSON.stringify(actual) === JSON.stringify(fixture.expected),
      ...(JSON.stringify(actual) === JSON.stringify(fixture.expected) ? {} : { expected: fixture.expected, actual }),
    });
  }
  return { schemaVersion: document.schemaVersion, status: results.every((item) => item.pass) ? "passed" : "failed", fixtures: results };
}

export function loadRepositoryFixtureManifest(path) {
  try { return JSON.parse(readFileSync(path, "utf8")); }
  catch (cause) { fail("REPOSITORY_FIXTURE_INVALID", `cannot load JSON: ${cause.message}`, path); }
}

export function main(argv = process.argv.slice(2)) {
  if (argv.length !== 2) {
    process.stderr.write("Usage: node scripts/repository-fixtures.mjs <golden-repositories.json> <new-destination>\n");
    return 2;
  }
  try {
    const result = materializeRepositoryFixtures(loadRepositoryFixtureManifest(resolve(argv[0])), resolve(argv[1]));
    process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    return result.status === "passed" ? 0 : 1;
  } catch (cause) {
    const normalized = cause instanceof RepositoryFixtureError
      ? cause
      : new RepositoryFixtureError("REPOSITORY_FIXTURE_INVALID", cause.message);
    process.stdout.write(`${JSON.stringify({ status: "invalid", errors: [normalized.toJSON()] }, null, 2)}\n`);
    return 1;
  }
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) process.exitCode = main();
