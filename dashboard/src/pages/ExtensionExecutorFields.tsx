import { ConfigurationForm, type ConfigurationSchema } from "../components/extensions/ConfigurationForm";
import type { NodeExecutor } from "./workflowEditorModel";

type ExtensionExecutor = Extract<NodeExecutor, { type: "extension" }>;

export function ExtensionExecutorFields({ value, schema, onChange }: {
  value: ExtensionExecutor; schema?: ConfigurationSchema; onChange(value: ExtensionExecutor): void;
}) {
  return <section><h3>Extension configuration</h3>{(["id", "version", "digest"] as const).map(key => <label className="field" key={key}><span>{key === "id" ? "Extension ID" : key === "version" ? "Exact version" : "Implementation digest"}</span><input value={value.ref[key]} onChange={event => onChange({ ...value, ref: { ...value.ref, [key]: event.target.value } })} /></label>)}
    <ConfigurationForm value={value.configuration} schema={schema} onChange={configuration => onChange({ ...value, configuration })} />
  </section>;
}
