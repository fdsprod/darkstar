// Strict YAML-subset parser for the ```apispec / ```datamodel fence bodies, plus the schema
// mappers that turn parsed data into props for the existing ApiSpec/DataModel renderers in
// components.js. The subset is deliberately tiny so the format stays deterministic for LLM
// authors and lintable by this file alone (the plugin ships zero-dependency — no yaml lib):
//
//   key: value                 top-level scalar (value = everything after the first colon;
//                              one surrounding pair of quotes is stripped)
//   key:                       container — followed by either:
//     - { k: v, k2: v2 }         a list of single-line flow mappings, or
//     200: value                 an indented plain map (the responses shape)
//   key: |                     block scalar (the example/schema slot — solves nested fences)
//   # comment                  skipped
//
// Anything else — anchors, aliases, `---` documents, nested block maps, multi-line flow —
// is a parse error with a line number. Schema violations (missing/unknown keys) come from
// the mappers. The viewer surfaces both as a visible warning: that warning is the lint.
//
// Loaded as a classic <script> in the browser (global.RichYamlLite) and importable from a
// node --test file via globalThis (see tests/unit/yaml-lite.test.mjs).

  const FLOW_ITEM_RE = /^-\s*\{(.*)\}\s*$/;

  function unquote(value) {
    const q = value[0];
    if ((q === '"' || q === "'") && value.length > 1 && value[value.length - 1] === q) {
      return value.slice(1, -1);
    }
    return value;
  }

  function coerce(value) {
    if (value === "true") {
      return true;
    }
    if (value === "false") {
      return false;
    }
    return unquote(value);
  }

  // Splits flow-mapping content on commas that are outside quotes. A value containing a
  // comma must be quoted; a colon after the entry's first colon needs no quoting.
  function splitFlowEntries(content) {
    const entries = [];
    let current = "";
    let quote = null;
    for (let i = 0; i < content.length; i++) {
      const ch = content[i];
      if (quote) {
        current += ch;
        if (ch === quote) {
          quote = null;
        }
        continue;
      }
      if (ch === '"' || ch === "'") {
        quote = ch;
        current += ch;
        continue;
      }
      if (ch === ",") {
        entries.push(current);
        current = "";
        continue;
      }
      current += ch;
    }
    entries.push(current);
    return entries.map((e) => e.trim()).filter((e) => e !== "");
  }

  function parseFlowMapping(content, lineNo, errors) {
    const obj = {};
    for (const entry of splitFlowEntries(content)) {
      const ci = entry.indexOf(":");
      if (ci === -1) {
        errors.push("line " + lineNo + ': expected "key: value" inside { … }, got "' + entry + '"');
        continue;
      }
      const key = entry.slice(0, ci).trim();
      const value = entry.slice(ci + 1).trim();
      if (!/^\w+$/.test(key)) {
        errors.push("line " + lineNo + ': bad flow-mapping key "' + key + '"');
        continue;
      }
      if (key in obj) {
        errors.push("line " + lineNo + ': duplicate key "' + key + '" in flow mapping');
        continue;
      }
      obj[key] = coerce(value);
    }
    return obj;
  }

  function indentOf(line) {
    return line.length - line.trimStart().length;
  }

  // parse(source) -> { data, errors }. data maps top-level keys to a string, an array of
  // flat objects, or a plain map. errors is a list of "line N: …" strings; on any error the
  // caller should treat the whole block as invalid.
  function parse(source) {
    const lines = source.replace(/\r\n/g, "\n").split("\n");
    const data = {};
    const errors = [];
    let i = 0;

    while (i < lines.length) {
      const line = lines[i];
      const lineNo = i + 1;
      if (line.trim() === "" || line.trim().startsWith("#")) {
        i++;
        continue;
      }
      if (line.trim() === "---") {
        errors.push("line " + lineNo + ": multi-document YAML is not supported");
        i++;
        continue;
      }
      if (indentOf(line) > 0) {
        errors.push("line " + lineNo + ": unexpected indented line (no containing key)");
        i++;
        continue;
      }
      const m = /^(\w+):(.*)$/.exec(line);
      if (!m) {
        errors.push("line " + lineNo + ': expected "key: value", got "' + line.trim() + '"');
        i++;
        continue;
      }
      const key = m[1];
      const after = m[2].trim();
      if (key in data) {
        errors.push("line " + lineNo + ': duplicate key "' + key + '"');
      }
      i++;

      // Block scalar — captures everything indented under `key: |` verbatim (dedented by the
      // first content line's indent), so an example may contain fences, colons, anything.
      if (after === "|") {
        const blockLines = [];
        while (i < lines.length && (lines[i].trim() === "" || indentOf(lines[i]) > 0)) {
          blockLines.push(lines[i]);
          i++;
        }
        while (blockLines.length && blockLines[blockLines.length - 1].trim() === "") {
          blockLines.pop();
        }
        const first = blockLines.find((l) => l.trim() !== "");
        const dedent = first ? indentOf(first) : 0;
        data[key] = blockLines.map((l) => l.slice(dedent)).join("\n");
        continue;
      }

      // Scalar
      if (after !== "") {
        if (after.startsWith("&") || after.startsWith("*")) {
          errors.push("line " + lineNo + ": anchors/aliases are not supported");
          continue;
        }
        data[key] = unquote(after);
        continue;
      }

      // Container — list of flow mappings or a plain indented map.
      const items = [];
      const map = {};
      let sawList = false;
      let sawMap = false;
      while (i < lines.length && (lines[i].trim() === "" || indentOf(lines[i]) > 0)) {
        const child = lines[i];
        const childNo = i + 1;
        i++;
        if (child.trim() === "" || child.trim().startsWith("#")) {
          continue;
        }
        const flowItem = FLOW_ITEM_RE.exec(child.trim());
        if (flowItem) {
          sawList = true;
          items.push(parseFlowMapping(flowItem[1], childNo, errors));
          continue;
        }
        if (child.trim().startsWith("-")) {
          errors.push("line " + childNo + ': list items must be single-line flow mappings: "- { k: v, … }"');
          sawList = true;
          continue;
        }
        const entry = /^([\w-]+):(.*)$/.exec(child.trim());
        if (entry) {
          const value = entry[2].trim();
          if (value === "" || value === "|") {
            errors.push("line " + childNo + ": nested block maps/scalars are not supported");
            continue;
          }
          sawMap = true;
          map[entry[1]] = unquote(value);
          continue;
        }
        errors.push("line " + childNo + ': expected "- { … }" or "key: value", got "' + child.trim() + '"');
      }
      if (sawList && sawMap) {
        errors.push('key "' + key + '" mixes list items and map entries');
      }
      if (!sawList && !sawMap) {
        errors.push('key "' + key + '" has no value');
        continue;
      }
      data[key] = sawList ? items : map;
    }

    return { data, errors };
  }

  // required accepts booleans (the documented form) and the strings the renderers historically
  // displayed; renderers test /required/i, so normalize to those display strings.
  function requiredLabel(value) {
    if (value === true || value === "true" || value === "required") {
      return "required";
    }
    return "optional";
  }

  function checkKeys(data, known, errors) {
    for (const key of Object.keys(data)) {
      if (!known.includes(key)) {
        errors.push('unknown key "' + key + '" (expected: ' + known.join(", ") + ")");
      }
    }
  }

  function requireString(data, key, errors) {
    if (typeof data[key] !== "string" || data[key] === "") {
      errors.push('missing required key "' + key + '"');
      return "";
    }
    return data[key];
  }

  function itemList(data, key, requiredItemKeys, errors) {
    if (!(key in data)) {
      return [];
    }
    if (!Array.isArray(data[key])) {
      errors.push('"' + key + '" must be a list of "- { … }" flow mappings');
      return [];
    }
    return data[key].map((item, idx) => {
      for (const k of requiredItemKeys) {
        if (typeof item[k] !== "string" || item[k] === "") {
          errors.push('"' + key + '" item ' + (idx + 1) + ': missing "' + k + '"');
        }
      }
      return item;
    });
  }

  // mapApiSpec(data) -> { props, errors } where props feed RichComponents.ApiSpec directly.
  function mapApiSpec(data) {
    const errors = [];
    checkKeys(data, ["method", "path", "auth", "summary", "parameters", "responses", "example"], errors);
    const method = requireString(data, "method", errors);
    const path = requireString(data, "path", errors);
    const parameters = itemList(data, "parameters", ["name", "in", "type"], errors).map((p) => ({
      name: p.name || "",
      in: p.in || "",
      type: p.type || "",
      required: requiredLabel(p.required),
      description: p.description || "",
    }));
    let responses = [];
    if ("responses" in data) {
      if (Array.isArray(data.responses) || typeof data.responses !== "object") {
        errors.push('"responses" must be an indented map of "code: description" lines');
      } else {
        responses = Object.entries(data.responses).map(([code, description]) => {
          if (!/^\d{3}$/.test(code)) {
            errors.push('"responses" key "' + code + '" is not a 3-digit status code');
          }
          return { code, description };
        });
      }
    }
    return {
      props: {
        method,
        path,
        auth: typeof data.auth === "string" ? data.auth : null,
        description: typeof data.summary === "string" ? data.summary : "",
        parameters,
        responses,
        schema: typeof data.example === "string" ? data.example : null,
      },
      errors,
    };
  }

  // mapDataModel(data) -> { props, errors } feeding RichComponents.DataModel.
  function mapDataModel(data) {
    const errors = [];
    checkKeys(data, ["name", "store", "summary", "fields", "relationships", "example"], errors);
    const name = requireString(data, "name", errors);
    const fields = itemList(data, "fields", ["name", "type"], errors).map((f) => ({
      name: f.name || "",
      type: f.type || "",
      required: requiredLabel(f.required),
      default: f.default || "",
      description: f.description || "",
    }));
    const relationships = itemList(data, "relationships", ["relation", "target"], errors).map((r) => ({
      relation: r.relation || "",
      target: r.target || "",
      cardinality: r.cardinality || "",
      description: r.description || "",
    }));
    return {
      props: {
        name,
        store: typeof data.store === "string" ? data.store : null,
        description: typeof data.summary === "string" ? data.summary : "",
        fields,
        relationships,
        example: typeof data.example === "string" ? data.example : null,
      },
      errors,
    };
  }

export { parse, mapApiSpec, mapDataModel };
