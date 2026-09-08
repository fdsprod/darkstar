import test from 'node:test';
import assert from 'node:assert/strict';
import {createStarterDocument, deriveEditorGraph, normalizeLayout} from '../src/pages/workflowEditorModel.ts';
import {addResource, addArtifactOutput, compareWorkflowVersions} from '../src/pages/workflowResourceModel.ts';
import {autoLayoutGraph,derivePortGraph,bindPorts} from '../src/pages/workflowPortModel.ts';
test('resources have independent positions and remain upstream of their consumers',()=>{
 let doc=createStarterDocument('test/resources');for(const kind of ['task','template','repository'])doc=addResource(doc,kind).document;
 doc=addArtifactOutput(doc,'start','document.md').document;
 doc.spec.nodes.start.inputs={task:{type:'task',from:'run.input.task'},template:{type:'template',from:'run.input.template'}};
 const graph=deriveEditorGraph(doc,{}),ports=derivePortGraph(doc,graph).ports;
 const positions=autoLayoutGraph(normalizeLayout({}),graph,ports).nodes;
 assert.equal(new Set(Object.values(positions).map(p=>`${p.x}:${p.y}`)).size,Object.keys(positions).length);
 assert.ok(positions['$input:template'].x<positions.start.x);assert.ok(positions['$output:start:document'].x>positions.start.x);
});
test('wiring an artifact producer rewrites consumers to the typed output',()=>{
 let doc=addResource(createStarterDocument('test/artifact'),'artifact','document.md').document;
 doc.spec.nodes.start.outputs={document:{type:'markdown'}};
 doc.spec.nodes.start.inputs={previous:{type:'markdown',from:'run.input.artifact',required:false}};
 const ports=derivePortGraph(doc,deriveEditorGraph(doc,{})).ports;
 const result=bindPorts(doc,ports.find(p=>p.kind==='data'&&p.direction==='output'),ports.find(p=>p.portId==='$write'),ports);
 assert.equal(result.kind,'changed');assert.equal(result.document.spec.inputs.artifact,undefined);
 assert.equal(result.document.spec.nodes.start.outputs.document.artifact.filename,'document.md');
 assert.equal(result.document.spec.nodes.start.inputs.previous.from,'node.start.output.document');
});

test('version picker puts the current semantic version first',()=>{assert.deepEqual(['1.9.0','2.0.0-beta.1','2.0.0','1.10.0'].sort((a,b)=>compareWorkflowVersions(b,a)),['2.0.0','2.0.0-beta.1','1.10.0','1.9.0']);});

 test('nominal resource types reject structurally similar resources',()=>{
 let doc=createStarterDocument('test/nominal');for(const kind of ['template','repository','task'])doc=addResource(doc,kind).document;
 doc.spec.nodes.start.inputs={template:{type:'template',from:'run.input.template'}};
 const ports=derivePortGraph(doc,deriveEditorGraph(doc,{})).ports,target=ports.find(p=>p.kind==='data'&&p.portId==='template');
 for(const id of ['task','repository'])assert.equal(bindPorts(doc,ports.find(p=>p.kind==='run_input'&&p.portId===id),target,ports).kind,'invalid');
 assert.equal(bindPorts(doc,ports.find(p=>p.kind==='run_input'&&p.portId==='template'),target,ports).kind,'changed');
 });

test('Markdown variables require a filename before creation',()=>{const doc=createStarterDocument('test/filename');for(const filename of ['', ' ', '../design.md','design.txt']){assert.throws(()=>addResource(doc,'artifact',filename));assert.throws(()=>addArtifactOutput(doc,'start',filename));}assert.equal(addResource(doc,'artifact','design.md').document.spec.inputs.artifact.resource.filename,'design.md');assert.equal(addArtifactOutput(doc,'start','design.md').document.spec.nodes.start.outputs.design.artifact.filename,'design.md');assert.equal(doc.spec.inputs?.artifact,undefined);});
