// Decode transport envelopes only when the entire response is valid JSON.
// Plain Markdown and incomplete streamed JSON remain intact; never replace escapes blindly.
export type ReadableValue = {kind:'markdown';text:string} | {kind:'fields';fields:{name:string;value:ReadableValue}[]} | {kind:'json';value:unknown};
export function readableValue(value:unknown):ReadableValue {
 if(typeof value==='string')return {kind:'markdown',text:value};
 if(value&&typeof value==='object'&&!Array.isArray(value))return {kind:'fields',fields:Object.entries(value).map(([name,item])=>({name,value:readableValue(item)}))};
 return {kind:'json',value};
}
export function readableResponse(text:string):ReadableValue {
 try {return readableValue(JSON.parse(text));} catch {return {kind:'markdown',text};}
}
