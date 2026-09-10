import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import { dirname, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

const root = fileURLToPath(new URL("../", import.meta.url));
const source = resolve(root, "src");

async function walk(directory) {
  const entries = await readdir(directory, { withFileTypes: true });
  const found = await Promise.all(entries.map((entry) => {
    const path = resolve(directory, entry.name);
    return entry.isDirectory() ? walk(path) : [path];
  }));
  return found.flat();
}

const files = await walk(source);
const modules = files.filter((path) => /\.tsx?$/.test(path));
const stories = modules.filter((path) => path.endsWith(".stories.tsx"));
const slashed = (path) => path.split("\\").join("/");
// Everything under src/components is pure except these known wrappers, which
// bind the router and shared state for the pure components beneath them.
const wrappers = new Set(["components/AppShell.tsx", "components/PageStructure.tsx"]);
const pure = modules.filter((path) => {
  const name = slashed(relative(source, path));
  return name.startsWith("components/") && !wrappers.has(name);
});

const slash = (path) => slashed(relative(source, path));

/** Resolves an import specifier to a repository-relative source path. */
function target(from, specifier) {
  if (!specifier.startsWith(".")) return specifier;
  return slash(resolve(dirname(from), specifier));
}

async function imports(path) {
  const text = await readFile(path, "utf8");
  return [...text.matchAll(/(?:from|import)\s*\(?\s*["']([^"']+)["']/g)].map((match) => target(path, match[1]));
}

// The folder is a convention; this rule is the boundary. A pure component that
// reaches for the client, shared state, or the router stops being previewable.
// Generated schema types carry no behavior, so they stay allowed.
const forbidden = [/^api\/client$/, /^api\/bootstrap$/, /^state\//, /^app\/router$/];

test("pure components never depend on the client, shared state, or the router", async () => {
  assert.ok(pure.length >= 15, `expected a populated pure component layer, found ${pure.length}`);
  for (const path of pure) {
    for (const specifier of await imports(path)) {
      for (const rule of forbidden) {
        assert.ok(!rule.test(specifier), `${slash(path)} imports ${specifier}, which breaks the pure boundary`);
      }
    }
  }
});

test("pure components never perform side effects of their own", async () => {
  for (const path of pure.filter((file) => !file.endsWith(".stories.tsx"))) {
    const text = await readFile(path, "utf8");
    assert.doesNotMatch(text, /\bfetch\s*\(/, `${slash(path)} performs a network call`);
    assert.doesNotMatch(text, /localStorage|sessionStorage/, `${slash(path)} reads browser storage`);
  }
});

test("every story renders without the API, shared state, or the router", async () => {
  assert.ok(stories.length >= 8, `expected a populated story catalog, found ${stories.length}`);
  for (const path of stories) {
    for (const specifier of await imports(path)) {
      for (const rule of forbidden) {
        assert.ok(!rule.test(specifier), `${slash(path)} imports ${specifier}; a story must render in isolation`);
      }
    }
  }
});

test("every pure primitive is documented by a story", async () => {
  // A story is required where a styling pass needs to see the component; the
  // primitive and terminal layers are both covered.
  const documented = (name) => name.startsWith("components/ui/") || name.startsWith("components/terminal/");
  const components = pure.filter((path) => path.endsWith(".tsx") && !path.endsWith(".stories.tsx") && documented(slashed(relative(source, path))));
  assert.ok(components.length >= 8, `expected covered primitives, found ${components.length}`);
  for (const path of components) {
    const story = path.replace(/\.tsx$/, ".stories.tsx");
    assert.ok(files.includes(story), `${slash(path)} has no ${slash(story)}`);
  }
});

test("Storybook discovers colocated stories and loads the real stylesheet", async () => {
  const [main, preview] = await Promise.all([
    readFile(new URL("../.storybook/main.ts", import.meta.url), "utf8"),
    readFile(new URL("../.storybook/preview.tsx", import.meta.url), "utf8"),
  ]);
  assert.match(main, /\.\.\/src\/\*\*\/\*\.stories/);
  assert.match(main, /@storybook\/react-vite/);
  assert.match(main, /addon-a11y/);
  assert.match(preview, /import "\.\.\/src\/styles\.css"/);
});
