import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const read = (path) => readFile(new URL(path, import.meta.url), "utf8");

test("dashboard typography uses a readable semantic scale", async () => {
  const styles = await read("../src/styles.css");
  for (const token of ["page-title", "section-title", "body", "label", "caption", "control", "mono"]) {
    assert.match(styles, new RegExp(`--font-${token}:`), `missing ${token} typography token`);
  }
  const pixelSizes = [...styles.matchAll(/font-size:\s*([\d.]+)px/g)].map((match) => Number(match[1]));
  assert.ok(pixelSizes.every((size) => size >= 12), `found unreadable font size: ${Math.min(...pixelSizes)}px`);
  assert.match(styles, /--font-body:\s*\.875rem/);
  assert.match(styles, /--font-caption:\s*\.75rem/);
  assert.match(styles, /--control-height:\s*2\.5rem/);
  assert.match(styles, /overflow-x:\s*hidden/);
  assert.match(styles, /code, pre \{[^}]*--font-mono[^}]*overflow-wrap: anywhere/s);
});
