import test from 'node:test';
import assert from 'node:assert/strict';
import {sourceLines,markdownHeadings,tableCells} from '../src/components/document/markdownModel.ts';
import {parse,mapApiSpec,mapDataModel} from '../src/components/document/structuredYaml.js';
test('source offsets preserve Unicode, CRLF and repeated headings without indexing fenced headings',()=>{
 const text='# Same\r\n😀 line\r\n```md\r\n# Hidden\r\n```\r\n# Same';
 const headings=markdownHeadings(text);assert.equal(headings.length,2);assert.notEqual(headings[0].id,headings[1].id);
 for(const line of sourceLines(text))assert.equal(text.slice(line.start,line.start+line.text.length),line.text);
 assert.equal(text.slice(headings[1].start),'# Same');
});
test('table cell offsets retain escaped and code pipes',()=>{
 const text='| a\\|b | `x|y` |';const cells=tableCells({text,start:12});assert.deepEqual(cells.map(c=>c.text),['a\\|b','`x|y`']);for(const c of cells)assert.equal(text.slice(c.start-12,c.start-12+c.text.length),c.text);
});
test('structured fences validate the authoring contract and reject unknown keys',()=>{
 const api=parse('method: PATCH\npath: /users/:id\nparameters:\n  - { name: id, in: path, type: string, required: true }\nresponses:\n  200: Updated');assert.deepEqual(api.errors,[]);assert.deepEqual(mapApiSpec(api.data).errors,[]);
 assert.ok(mapDataModel(parse('name: User\nunexpected: value').data).errors.length);
 assert.ok(parse('name: User\nfields:\n  - name: id\n    type: uuid').errors.length);
});
