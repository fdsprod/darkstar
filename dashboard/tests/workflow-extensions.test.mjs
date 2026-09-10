import test from 'node:test';
import assert from 'node:assert/strict';
import { addNode, createStarterDocument, inspectNode, updateNodeExecutor, updateNodeShared } from '../src/pages/workflowEditorModel.ts';

test('custom nodes and validators retain nested configuration and exact pins', () => {
  let { document, nodeId } = addNode(createStarterDocument('extensions'), 'extension', 'custom');
  const extension = { ref: { id: 'example/custom', version: '1.0.0', digest: 'a'.repeat(64) }, configuration: { nested: { future: ['preserve', 2] } } };
  document = updateNodeExecutor(document, nodeId, { type: 'extension', ...extension });
  assert.equal(document.apiVersion, 'darkstar.local/v1alpha3');
  assert.deepEqual(inspectNode(document,nodeId).executor, { type: 'extension', ...extension });
  document = updateNodeShared(document,nodeId,{kind:'validators',value:[{kind:'extension',extension}]});
  const node = inspectNode(document,nodeId);
  assert.equal(node.validators[0].kind,'extension');
  assert.deepEqual(node.validators[0].extension,extension);
});
