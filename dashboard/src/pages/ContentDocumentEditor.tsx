import { Markdown } from "../components/Markdown";
import type { ContentDocument, PromptCondition } from "./contentLibraryModel";

export function ContentDocumentEditor({ document, readOnly = false, onChange }: {
  document: ContentDocument;
  readOnly?: boolean;
  onChange(document: ContentDocument): void;
}) {
  const text = document.kind === "template" ? document.content : document.instructions;
  return <div className="content-document-editor">
    <fieldset disabled={readOnly}>
      <legend>{document.kind === "template" ? "Document structure" : "Task instructions"}</legend>
      <label className="field"><span>{document.kind === "template" ? "Markdown template" : "Base instructions"}</span><textarea rows={16} value={text} onChange={(event) => {
        onChange(document.kind === "template" ? { ...document, content: event.target.value } : { ...document, instructions: event.target.value });
      }} /></label>
      {document.kind === "template" ? <label className="field"><span>Required headings, one per line</span><textarea value={(document.requiredHeadings ?? []).join("\n")} onChange={(event) => {
        onChange({ ...document, requiredHeadings: event.target.value.split("\n").filter((line) => line.trim()) });
      }} /></label> : <>
        {(document.sections ?? []).map((section, index) => {
          const update = (changes: Partial<typeof section>) => {
            onChange({ ...document, sections: document.sections?.map((candidate, at) => at === index ? { ...candidate, ...changes } : candidate) });
          };
          return <fieldset key={index}><legend>Conditional section {index + 1}</legend>
            <label className="field"><span>Section identifier</span><input value={section.id} onChange={(event) => {
              update({ id: event.target.value });
            }} /></label>
            <label className="field"><span>Include when</span><select value={section.when.kind} onChange={(event) => {
              const kind = event.target.value as PromptCondition["kind"];
              update({ when: kind === "revision" || kind === "always" ? { kind } : { kind, input: "input" in section.when ? section.when.input : "open_items" } });
            }}><option value="input_linked">Input is linked</option><option value="input_absent">Input is not linked</option><option value="revision">Revising an artifact</option><option value="always">Always</option></select></label>
            {"input" in section.when && <label className="field"><span>Input name</span><input list="prompt-input-names" value={section.when.input} onChange={(event) => {
              if ("input" in section.when) {
                update({ when: { ...section.when, input: event.target.value } });
              }
            }} /></label>}
            <label className="field"><span>Conditional instructions</span><textarea rows={5} value={section.instructions} onChange={(event) => {
              update({ instructions: event.target.value });
            }} /></label>
            <button type="button" onClick={() => {
              onChange({ ...document, sections: document.sections?.filter((_, at) => at !== index) });
            }}>Remove section</button>
          </fieldset>;
        })}
        <datalist id="prompt-input-names"><option value="open_items" /><option value="deferred_work" /></datalist>
        <button type="button" onClick={() => {
          const ids = new Set(document.sections?.map((section) => section.id));
          let number = 1;
          while (ids.has(`section_${number}`)) {
            number++;
          }
          onChange({ ...document, sections: [...(document.sections ?? []), { id: `section_${number}`, when: { kind: "input_linked", input: "open_items" }, instructions: "" }] });
        }}>Add conditional section</button>
      </>}
    </fieldset>
    <section aria-label="Rich Artifacts preview"><h3>Preview</h3><Markdown text={text} /></section>
  </div>;
}
