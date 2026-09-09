import test from 'node:test';
import assert from 'node:assert/strict';
import {readableResponse} from '../src/components/document/responseModel.ts';
test('document JSON envelopes decode generically without changing source text',()=>{
 const research='# Research\n\nBody with `C:\\repo` and literal \\n.',design='## Design\n\n- [ ] Check';
 const parsed=readableResponse(JSON.stringify({research,design,score:4}));assert.equal(parsed.kind,'fields');assert.deepEqual(parsed.fields[0],{name:'research',value:{kind:'markdown',text:research}});assert.equal(parsed.fields[1].value.text,design);assert.deepEqual(parsed.fields[2].value,{kind:'json',value:4});
});
test('plain Markdown and incomplete streams never undergo escape replacement',()=>{
 for(const text of ['# Hello\n\n`work_123`','{"research":"# unfinished\\n','literal \\n content'])assert.deepEqual(readableResponse(text),{kind:'markdown',text});
});
