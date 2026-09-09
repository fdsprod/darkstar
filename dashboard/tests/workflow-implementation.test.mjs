import test from 'node:test';
import assert from 'node:assert/strict';
import {addNode,createStarterDocument,inspectNode,updateNodeExecutor} from '../src/pages/workflowEditorModel.ts';

test('Implementation connects a task and changeset without a required plan',()=>{
 const {document,nodeId}=addNode(createStarterDocument('example'),'implementation');
 assert.equal(document.apiVersion,'darkstar.local/v1alpha3');
 const node=inspectNode(document,nodeId);
 assert.equal(node.executor.type,'implementation');
 assert.equal(node.executor.taskInput,'task');
 assert.deepEqual(document.spec.nodes[nodeId].permissions,['process.run','workspace.write']);
 assert.equal(document.spec.nodes[nodeId].outputs.changeset.type,'object');
 assert.equal(document.spec.inputs.task.resource.kind,'task');
 assert.equal(document.spec.inputs.plan,undefined);
 const updated=updateNodeExecutor(document,nodeId,{...node.executor,instructions:'Update README.md on disk.'});
 assert.equal(inspectNode(updated,nodeId).executor.instructions,'Update README.md on disk.');
});
