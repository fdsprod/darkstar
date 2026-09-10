import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtemp, writeFile, unlink, rmdir } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { lintFiles } from '../scripts/lint-control-flow.mjs';

test('AST lint enforces readable control flow without interpreting strings or loop separators', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'darkstar-plugin-lint-'));
  const fixtures = {
    good: `const text = "if (x) return; first(); second();";
// if (hidden) return;
function run() {
  for (let index = 0; index < 3; index++) {
    console.log(index);
  }
  if (text) {
    return text;
  } else {
    return "empty";
  }
}
const callback = () => {
  return run();
};
`,
    unbraced: `function run() {
  if (true) return;
  while (false) console.log("loop");
}
`,
    compact: `function run() { console.log("first"); }
const callback = () => 1;
`,
    multiple: `console.log("first"); console.log("second");
const first = 1, second = 2;
(console.log(first), console.log(second));
`,
    inlineCase: `switch (1) {
  case 1: console.log("inline");
}
`,
  };
  const files = Object.keys(fixtures).map(name => join(directory, name + '.ts'));
  try {
    for (const [name, source] of Object.entries(fixtures)) {
      await writeFile(join(directory, name + '.ts'), source);
    }
    const violations = lintFiles(files);
    const messages = name => violations.filter(item => item.file === join(directory, name + '.ts')).map(item => item.message);
    assert.deepEqual(messages('good'), []);
    assert.ok(messages('unbraced').some(message => message.includes('require braces')));
    assert.ok(messages('compact').some(message => message.includes('multiple lines')));
    assert.ok(messages('compact').some(message => message.includes('require braces')));
    assert.ok(messages('multiple').some(message => message.includes('each statement')));
    assert.ok(messages('multiple').some(message => message.includes('one variable')));
    assert.ok(messages('multiple').some(message => message.includes('Sequence operations')));
    assert.ok(messages('inlineCase').some(message => message.includes('below the case label')));
  } finally {
    for (const file of files) {
      await unlink(file);
    }
    await rmdir(directory);
  }
});
