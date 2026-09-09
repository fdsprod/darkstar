import {useState} from "react";
import type { SourceRenderer } from '../Markdown';
import type { SourceLine } from './markdownModel';
import {parse,mapApiSpec,mapDataModel} from './structuredYaml.js';
import {MermaidDiagram} from './MermaidDiagram';
type CodeProps = {info:string;lines:SourceLine[];render:SourceRenderer};
export function CodeBlock(props:CodeProps) {
  const [copied,setCopied]=useState(false);
  return <CodeBlockView {...props} copied={copied} onCopy={()=>{void navigator.clipboard.writeText(props.lines.map(l=>l.text).join("\n")).then(()=>setCopied(true)).catch(()=>setCopied(false));}}/>;
}
export function CodeBlockView({info,lines,render,copied,onCopy}: CodeProps & {copied:boolean;onCopy():void}) {
  const body=<pre><code>{lines.map((line,i)=><span key={line.start} className={line.text.startsWith('#!')?'code-note':info.startsWith('diff')?(line.text.startsWith('+')?'diff-added':line.text.startsWith('-')?'diff-removed':undefined):undefined}>{render(line.text,line.start)}{i<lines.length-1?'\n':''}</span>)}</code></pre>;
  return <section className="markdown-code"><header><span>{info || 'text'}</span><span>{lines.length} lines</span><button type="button" onClick={onCopy}>{copied?"Copied":"Copy"}</button></header>{lines.length>24?<details><summary>Show code · {lines.length} lines</summary>{body}</details>:body}</section>;
}
export function FileTree({lines,render}: {lines:SourceLine[];render:SourceRenderer}) {return <div className="markdown-tree" role="list" aria-label="File tree">{lines.map(line=><div role="listitem" key={line.start} style={{paddingLeft:`${(/^ */.exec(line.text)![0].length/2)*16}px`}} className={/\[deleted\]/.test(line.text)?'diff-removed':/\[new\]/.test(line.text)?'diff-added':undefined}>{render(line.text,line.start)}</div>)}</div>;}
export function StructuredCard({title,values}: {title:string;values:Record<string,unknown>}) {
  return <section className="markdown-structured"><h3>{title}</h3>{Object.entries(values).filter(([,v])=>v!==null&&v!==''&&!(Array.isArray(v)&&!v.length)).map(([key,value])=><div key={key}><h4>{key}</h4>{Array.isArray(value)?<div className="markdown-table"><table><thead><tr>{Object.keys(value[0]??{}).map(k=><th key={k}>{k}</th>)}</tr></thead><tbody>{value.map((row,i)=><tr key={i}>{Object.entries(row as Record<string,unknown>).map(([k,v])=><td key={k}>{String(v??'')}</td>)}</tr>)}</tbody></table></div>:<p>{String(value)}</p>}</div>)}</section>;
}
export function StructuredFence(props: {info:string;lines:SourceLine[];render:SourceRenderer}) {
  const {info,lines,render}=props,language=info.split(/\s/)[0],code=lines.map(l=>l.text).join('\n');
  if(language==='tree')return <FileTree lines={lines} render={render}/>;
  if(language==='mermaid')return <section><MermaidDiagram code={code}/><details><summary>Diagram source · select text to annotate</summary><CodeBlock {...props}/></details></section>;
  if(language==='apispec'||language==='datamodel'){
    const parsed=parse(code),mapped=(language==='apispec'?mapApiSpec:mapDataModel)(parsed.data),errors=[...parsed.errors,...mapped.errors];
    return <section>{errors.length?<div className="markdown-alert markdown-alert--warning" role="status"><strong>Invalid {language}</strong><ul>{errors.map((e,i)=><li key={i}>{e}</li>)}</ul><CodeBlock {...props}/></div>:<><StructuredCard title={language==='apispec'?'Endpoint':'Data model'} values={mapped.props}/><details><summary>Component source · select text to annotate</summary><CodeBlock {...props}/></details></>}</section>;
  }
  return <CodeBlock {...props}/>;
}
