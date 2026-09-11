import test from 'node:test';
import assert from 'node:assert/strict';
import { componentCategories, executionKinds } from '../../plugins/builtin-nodes/src/execution-kinds.ts';
import { componentBadgeLabels, resourceCategory } from '../../packages/plugin-sdk/src/component-metadata.ts';

test('every built-in component declares a badge without changing its execution interface', () => {
  assert.deepEqual(Object.keys(componentCategories).sort(),Object.keys(executionKinds).sort());
  assert.equal(componentBadgeLabels[componentCategories.approval],'HITL');
  for (const kind of ['start','done','gate','routing','subworkflow']) {
    assert.equal(componentBadgeLabels[componentCategories[kind]],'CONTROL');
  }
  assert.equal(componentBadgeLabels[componentCategories.implementation],'LLM');
  assert.equal(componentBadgeLabels[componentCategories.create_pr],'DETERMINISTIC');
});

test('resource and output cards distinguish documents and templates from data', () => {
  for (const kind of ['task','repository','workspace','schema:changeset_v1','schema:commit_v1','schema:pull_request_v1','open_items','decision_log']) {
    assert.equal(componentBadgeLabels[resourceCategory(kind,kind)],'DATA');
  }
  assert.equal(componentBadgeLabels[resourceCategory('artifact','markdown')],'ARTIFACT');
  assert.equal(componentBadgeLabels[resourceCategory('value','markdown')],'ARTIFACT');
  assert.equal(componentBadgeLabels[resourceCategory('template','template')],'TEMPLATE');
});
