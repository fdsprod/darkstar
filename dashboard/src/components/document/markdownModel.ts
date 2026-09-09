export type SourceLine = { text: string; start: number };
export type Heading = { id: string; level: number; text: string; start: number };
// Offsets always address the original UTF-16 string, including CRLF. Never normalize it.
export function sourceLines(text: string): SourceLine[] {
  const lines: SourceLine[] = []; const re = /([^\r\n]*)(\r\n|\n|\r|$)/g;
  for (const match of text.matchAll(re)) { if (!match[0]) break; lines.push({ text: match[1], start: match.index! }); }
  return lines;
}
export function markdownHeadings(text: string): Heading[] {
  let fence = ''; const result: Heading[] = [];
  for (const line of sourceLines(text)) {
    const f = /^\s*(`{3,}|~{3,})/.exec(line.text);
    if (f) { if (!fence) fence = f[1]; else if (f[1][0] === fence[0] && f[1].length >= fence.length) fence = ''; continue; }
    if (fence) continue;
    const h = /^(#{1,6})\s+(.*)/.exec(line.text);
    if (h) result.push({ id: `md-${line.start}`, level: h[1].length, text: h[2].replace(/[*_`]/g, ''), start: line.start });
  }
  return result;
}
export function tableCells(line: SourceLine): SourceLine[] {
  const result: SourceLine[] = []; let start = 0, ticks = 0;
  for (let i = 0; i <= line.text.length; i++) {
    if (line.text[i] === '`') ticks ^= 1;
    if (i !== line.text.length && (line.text[i] !== '|' || ticks || line.text[i - 1] === '\\')) continue;
    const raw = line.text.slice(start, i), trim = raw.trim();
    if (trim || (start !== 0 && i !== line.text.length)) result.push({ text: trim, start: line.start + start + raw.indexOf(trim) });
    start = i + 1;
  }
  return result;
}
// Read a DOM endpoint through source-addressed text leaves. Syntax hidden by formatting
// stays in the durable range between endpoints; repeated phrases never use text search.
export function sourceEndpoint(root: HTMLElement, node: Node, offset: number, end: boolean): number | undefined {
  const element = node.nodeType === 1 ? node as Element : node.parentElement;
  const leaf = element?.closest<HTMLElement>('[data-source-start]');
  if (leaf && root.contains(leaf)) {
    const range = document.createRange(); range.selectNodeContents(leaf); range.setEnd(node, offset);
    return Number(leaf.dataset.sourceStart) + range.toString().length;
  }
  if (node.nodeType === Node.TEXT_NODE) return undefined;
  const range = document.createRange(); range.selectNodeContents(root); range.setEnd(node, offset);
  const leaves = [...root.querySelectorAll<HTMLElement>('[data-source-start]')];
  const preceding = leaves.filter(item => range.intersectsNode(item));
  const target = end ? preceding.at(-1) : leaves.find(item => !preceding.includes(item));
  return target ? Number(target.dataset.sourceStart) + (end ? target.textContent?.length ?? 0 : 0) : undefined;
}
