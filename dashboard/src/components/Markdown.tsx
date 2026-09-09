import { type ReactNode } from 'react';
import { sourceLines, tableCells, type SourceLine } from './document/markdownModel';
import { CodeBlock, StructuredFence } from './document/MarkdownBlocks';
export type SourceRenderer = (text: string, start: number) => ReactNode;
const plain: SourceRenderer = (text, start) => <span data-source-start={start}>{text}</span>;
export function MarkdownInline({text, start, render = plain}: {text: string; start: number; render?: SourceRenderer}) {
  const parts: ReactNode[] = []; let cursor = 0;
  const re = /(`+)([^`\n]+)\1|\*\*([^\n]+?)\*\*|__([^\n]+?)__|\*([^*\n]+)\*|_([^_\n]+)_|~~([^\n]+?)~~|\[([^\]\n]+)\]\(([^\s)]+)\)|\\([\\`*_{}\[\]()#+.!|>-])/g;
  for (const m of text.matchAll(re)) {
    const at = m.index!; if (at > cursor) parts.push(<span key={cursor}>{render(text.slice(cursor, at), start + cursor)}</span>);
    let content: ReactNode;
    const child = (value: string, shift: number) => <MarkdownInline text={value} start={start + at + shift} render={render} />;
    if (m[1]) content = <code>{render(m[2], start + at + m[1].length)}</code>;
    else if (m[3] || m[4]) content = <strong>{child(m[3] || m[4], 2)}</strong>;
    else if (m[5] || m[6]) content = <em>{child(m[5] || m[6], 1)}</em>;
    else if (m[7]) content = <del>{child(m[7], 2)}</del>;
    else if (m[8] && /^(https?:\/\/|mailto:)/i.test(m[9])) content = <a href={m[9]} target="_blank" rel="noopener noreferrer">{child(m[8], 1)}</a>;
    else if (m[10]) content = render(m[10], start + at + 1);
    else content = render(m[0], start + at);
    parts.push(<span key={at}>{content}</span>); cursor = at + m[0].length;
  }
  if (cursor < text.length) parts.push(<span key={cursor}>{render(text.slice(cursor), start + cursor)}</span>);
  return <>{parts}</>;
}
function blocks(lines: SourceLine[], render: SourceRenderer): ReactNode[] {
  const result: ReactNode[] = [], inline = (line: SourceLine) => <MarkdownInline text={line.text} start={line.start} render={render} />;
  for (let i = 0; i < lines.length;) {
    const line = lines[i], key = line.start; if (!line.text.trim()) { i++; continue; }
    const fence = /^\s*(`{3,}|~{3,})(.*)$/.exec(line.text);
    if (fence) {
      const body: SourceLine[] = []; i++;
      while (i < lines.length && !new RegExp('^\\s*' + fence[1][0] + '{' + fence[1].length + ',}\\s*$').test(lines[i].text)) body.push(lines[i++]);
      if (i < lines.length) i++;
      result.push(<StructuredFence key={key} info={fence[2].trim()} lines={body} render={render} />); continue;
    }
    const h = /^(#{1,6})\s+(.*)$/.exec(line.text);
    if (h) { result.push(<div key={key} id={`md-${key}`} role="heading" aria-level={h[1].length} className={`markdown-heading markdown-heading--${h[1].length}`}>{inline({text:h[2],start:key+line.text.indexOf(h[2],h[1].length)})}</div>); i++; continue; }
    if (/^\s*([-*_])(?:\s*\1){2,}\s*$/.test(line.text)) {result.push(<hr key={key}/>);i++;continue;}
    if (line.text.includes('|') && i+1<lines.length && tableCells(lines[i+1]).every(c=>/^:?-{3,}:?$/.test(c.text))) {
      const header=tableCells(line),rows:SourceLine[][]=[];i+=2;
      while(i<lines.length&&lines[i].text.includes('|')&&lines[i].text.trim())rows.push(tableCells(lines[i++]));
      result.push(<div className="markdown-table" key={key}><table><thead><tr>{header.map(c=><th key={c.start}>{inline(c)}</th>)}</tr></thead><tbody>{rows.map((row,r)=><tr key={r}>{header.map((_,c)=><td key={c}>{row[c]&&inline(row[c])}</td>)}</tr>)}</tbody></table></div>);continue;
    }
    if (/^\s*>/.test(line.text)) {
      const quote:SourceLine[]=[];
      while(i<lines.length&&/^\s*>/.test(lines[i].text)){const l=lines[i++],prefix=/^\s*> ?/.exec(l.text)![0].length;quote.push({text:l.text.slice(prefix),start:l.start+prefix});}
      const alert=/^\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\]$/.exec(quote[0].text);
      result.push(<blockquote className={alert?`markdown-alert markdown-alert--${alert[1].toLowerCase()}`:undefined} key={key}>{alert&&<strong>{render(alert[1],quote[0].start+2)}</strong>}{blocks(alert?quote.slice(1):quote,render)}</blockquote>);continue;
    }
    const list=/^(\s*)([-*+] |\d+[.)] )(.*)$/.exec(line.text);
    if(list){
      const ordered=/^\d/.test(list[2]),indent=list[1].length,items:ReactNode[]=[];
      while(i<lines.length){
        const m=/^(\s*)([-*+] |\d+[.)] )(.*)$/.exec(lines[i].text);
        if(!m||m[1].length!==indent||/^\d/.test(m[2])!==ordered)break;
        const current=lines[i++],prefix=m[1].length+m[2].length,child:SourceLine[]=[];
        while(i<lines.length&&lines[i].text.trim()&&/^\s+/.exec(lines[i].text)?.[0].length!>indent){const l=lines[i++],shift=Math.min(prefix,/^\s*/.exec(l.text)![0].length);child.push({text:l.text.slice(shift),start:l.start+shift});}
        const task=/^\[([ xX])\] /.exec(m[3]),shift=task?4:0;
        items.push(<li key={current.start}>{task&&<input type="checkbox" checked={task[1]!==' '} readOnly aria-label={task[1]===' '?'Incomplete task':'Completed task'}/>} {inline({text:m[3].slice(shift),start:current.start+prefix+shift})}{blocks(child,render)}</li>);
      }
      result.push(ordered?<ol key={key}>{items}</ol>:<ul key={key}>{items}</ul>);continue;
    }
    const paragraph:ReactNode[]=[inline(line)];i++;
    while(i<lines.length&&lines[i].text.trim()&&!/^(#{1,6}\s|\s*[`~]{3}|\s*>|\s*[-*+] |\s*\d+[.)] )/.test(lines[i].text)&&!(lines[i].text.includes('|')&&i+1<lines.length&&tableCells(lines[i+1]).every(c=>/^:?-{3,}:?$/.test(c.text)))){paragraph.push(' ',inline(lines[i++]));}
    result.push(<p key={key}>{paragraph.map((p,n)=><span key={n}>{p}</span>)}</p>);
  }
  return result;
}
export function Markdown({text, renderSource=plain}: {text: string; renderSource?:SourceRenderer}) {return <div className="run-markdown">{blocks(sourceLines(text),renderSource)}</div>;}
