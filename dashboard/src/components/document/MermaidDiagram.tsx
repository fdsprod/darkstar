import { useEffect, useId, useState } from 'react';
type MermaidAPI = { initialize(options: object): void; render(id: string, code: string): Promise<{svg: string}> };
let loader: Promise<MermaidAPI> | undefined;
function load(): Promise<MermaidAPI> {
  return loader ??= new Promise((resolve,reject)=>{
    const script=document.createElement('script');script.src='/vendor/mermaid.min.js';
    script.onload=()=>{const api=(window as unknown as {mermaid:MermaidAPI}).mermaid;api.initialize({startOnLoad:false,securityLevel:'strict',theme:'dark',maxTextSize:50000,flowchart:{htmlLabels:false},secure:['securityLevel','startOnLoad','maxTextSize','flowchart']});resolve(api);};
    script.onerror=()=>{loader=undefined;reject(new Error('Diagram renderer unavailable'));};document.head.append(script);
  });
}
export function DiagramView({src,error}: {src?:string;error?:string}) {return error?<p role="status">{error} · source available below.</p>:src?<img className="markdown-diagram" src={src} alt="Mermaid diagram"/>:<p role="status">Rendering diagram…</p>;}
export function MermaidDiagram({code}: {code:string}) {
  const id=useId().replace(/[^a-zA-Z0-9]/g,''),[state,setState]=useState<{src?:string;error?:string}>({});
  useEffect(()=>{let current=true;setState({});void load().then(api=>api.render('diagram'+id,code)).then(({svg})=>{if(current)setState({src:'data:image/svg+xml;charset=utf-8,'+encodeURIComponent(svg)});}).catch(()=>{if(current)setState({error:'This diagram could not be rendered'});});return()=>{current=false;};},[id,code]);
  return <DiagramView {...state}/>;
}
