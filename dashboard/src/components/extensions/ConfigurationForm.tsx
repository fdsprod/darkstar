import { useEffect, useState } from "react";

type ObjectValue = Record<string, unknown>;
export interface ConfigurationSchema {
  properties?: Record<string, { type?: string; title?: string; description?: string; enum?: string[] }>;
}

// Pure authoring surface. Schema data cannot supply components or execute code.
// Unknown structures remain editable as JSON without dropping unknown fields.
export function ConfigurationForm({ value, schema, onChange }: {
  value: ObjectValue; schema?: ConfigurationSchema; onChange(value: ObjectValue): void;
}) {
  const encoded = JSON.stringify(value, null, 2);
  const [text, setText] = useState(encoded);
  const [error, setError] = useState("");
  useEffect(() => { setText(encoded); setError(""); }, [encoded]);
  const fields = Object.entries(schema?.properties ?? {});
  const simple = fields.length > 0 && fields.every(([, field]) => ["string", "boolean", "number", "integer"].includes(field.type ?? ""));
  if (simple) return <div>{fields.map(([key, field]) => <label className="field" key={key}><span>{field.title ?? key}</span>
    {field.type === "boolean" ? <input type="checkbox" checked={value[key] === true} onChange={event => onChange({ ...value, [key]: event.target.checked })} />
      : field.enum ? <select value={String(value[key] ?? "")} onChange={event => onChange({ ...value, [key]: event.target.value })}><option value="">Select…</option>{field.enum.map(option => <option key={option}>{option}</option>)}</select>
      : <input type={field.type === "string" ? "text" : "number"} step={field.type === "integer" ? 1 : "any"} value={String(value[key] ?? "")} onChange={event => {
        if (field.type === "string") onChange({ ...value, [key]: event.target.value });
        else if (event.target.value !== "" && Number.isFinite(event.target.valueAsNumber)) onChange({ ...value, [key]: event.target.valueAsNumber });
      }} />}{field.description && <small>{field.description}</small>}</label>)}</div>;
  return <div><label className="field"><span>Configuration JSON</span><textarea rows={10} value={text} onChange={event => setText(event.target.value)} /></label>
    <button className="button" type="button" onClick={() => {
      try { const parsed: unknown = JSON.parse(text); if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("Configuration must be an object."); onChange(parsed as ObjectValue); setError(""); }
      catch (reason) { setError(reason instanceof Error ? reason.message : "Invalid configuration."); }
    }}>Use configuration</button>{error && <p role="alert">{error}</p>}</div>;
}
