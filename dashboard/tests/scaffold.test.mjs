import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("dashboard document exposes the React mount point", async () => {
  const document = await readFile(new URL("../index.html", import.meta.url), "utf8");

  assert.match(document, /<div id="root"><\/div>/);
  assert.match(document, /src="\/src\/main\.tsx"/);
});

test("dashboard source mounts the application", async () => {
  const source = await readFile(new URL("../src/main.tsx", import.meta.url), "utf8");

  assert.match(source, /createRoot\(root\)\.render/);
  assert.match(source, /<App \/>/);
});
