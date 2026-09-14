// Provider output contracts are generated from the public versioned schema.
// Inlining their local references keeps embedded runtime validation independent
// of filesystem/network schema loading.
export function investigationOutputSchemas(document) {
  function inline(value, visiting = new Set()) {
    if (Array.isArray(value)) {
      return value.map(item => inline(item, visiting));
    }
    if (!value || typeof value !== "object") {
      return value;
    }
    if (value.$ref) {
      if (!value.$ref.startsWith("#/$defs/")) {
        throw new Error(`Investigation output schema contains an external reference: ${value.$ref}`);
      }
      if (visiting.has(value.$ref)) {
        throw new Error(`Investigation output schema contains a cycle: ${value.$ref}`);
      }
      const definition = document.$defs[value.$ref.slice("#/$defs/".length)];
      if (!definition) {
        throw new Error(`Investigation output schema reference is missing: ${value.$ref}`);
      }
      return inline(definition, new Set([...visiting, value.$ref]));
    }
    return Object.fromEntries(Object.entries(value).map(([key, child]) => [key, inline(child, visiting)]));
  }
  const result = {};
  for (const [kind, name] of [["repository", "RepositoryFindings"], ["synthesis", "InvestigationSynthesis"]]) {
    if (!document.$defs[name]) {
      throw new Error(`Missing investigation output contract ${name}`);
    }
    result[kind] = { $schema: document.$schema, ...inline(document.$defs[name]) };
  }
  return `${JSON.stringify(result, null, 2)}\n`;
}
