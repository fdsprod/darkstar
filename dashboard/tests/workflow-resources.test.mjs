import test from 'node:test';
import assert from 'node:assert/strict';
import {createStarterDocument, deriveEditorGraph, normalizeLayout} from '../src/pages/workflowEditorModel.ts';
import {addResource, addArtifactOutput} from '../src/pages/workflowResourceModel.ts';
import {autoLayoutGraph,derivePortGraph,bindPorts} from '../src/pages/workflowPortModel.ts';
test('resources have independent positions and remain upstream of their consumers',()=>{
 let doc=createStarterDocument('test/resources');for(const kind of ['task','template','repository'])doc=addResource(doc,kind).document;
 doc=addArtifactOutput(doc,'start').document;
 doc.spec.nodes.start.inputs={task:{type:'object',from:'run.input.task'},template:{type:'object',from:'run.input.template'}};
 const graph=deriveEditorGraph(doc,{}),ports=derivePortGraph(doc,graph).ports;
 const positions=autoLayoutGraph(normalizeLayout({}),graph,ports).nodes;
 assert.equal(new Set(Object.values(positions).map(p=>`${p.x}:${p.y}`)).size,Object.keys(positions).length);
 assert.ok(positions['$input:template'].x<positions.start.x);assert.ok(positions['$output:start:document'].x>positions.start.x);
});
test('wiring an artifact producer rewrites consumers to the typed output',()=>{
 let doc=addResource(createStarterDocument('test/artifact'),'artifact').document;
 doc.spec.nodes.start.outputs={document:{type:'string'}};
 doc.spec.nodes.start.inputs={previous:{type:'string',from:'run.input.artifact',required:false}};
 const ports=derivePortGraph(doc,deriveEditorGraph(doc,{})).ports;
 const result=bindPorts(doc,ports.find(p=>p.kind==='data'&&p.direction==='output'),ports.find(p=>p.portId==='$write'),ports);
 assert.equal(result.kind,'changed');assert.equal(result.document.spec.inputs.artifact,undefined);
 assert.equal(result.document.spec.nodes.start.outputs.document.artifact.filename,'document.md');
 assert.equal(result.document.spec.nodes.start.inputs.previous.from,'node.start.output.document');
});
