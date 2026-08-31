#!/usr/bin/env node

// Executable reference for DARKSTAR workflow v1alpha1. Executors are fixture
// backed so graph semantics can be tested without providers or shell commands.

import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { pathToFileURL } from "node:url";

const ID_RE = /^[a-z][a-z0-9_]{0,63}$/;
const NODE_TYPES = new Set(["reasoning", "gate", "command", "approval", "subworkflow"]);
const VALUE_TYPES = new Set(["null", "boolean", "integer", "number", "string", "array", "object"]);
const MISSING = Symbol("missing");

export class WorkflowError extends Error {
  constructor(code, message, location = "", details = {}) {
    super(message);
    this.code = code;
    this.location = location;
    this.details = details;
  }

  toJSON() {
    const value = { code: this.code, message: this.message };
    if (this.location) value.location = this.location;
    if (Object.keys(this.details).length) {
      value.details = Object.fromEntries(Object.keys(this.details).sort().map((key) => [key, this.details[key]]));
    }
    return value;
  }
}

function fail(code, message, location = "", details = {}) {
  return new WorkflowError(code, message, location, details);
}

function canonical(value) {
  if (value === null || typeof value === "string" || typeof value === "boolean") return JSON.stringify(value);
  if (typeof value === "number") {
    if (!Number.isFinite(value)) throw fail("WF_SCHEMA_INVALID", "non-finite numbers are not allowed");
    return Object.is(value, -0) ? "0" : JSON.stringify(value);
  }
  if (Array.isArray(value)) return `[${value.map(canonical).join(",")}]`;
  if (typeof value === "object") {
    return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonical(value[key])}`).join(",")}}`;
  }
  throw fail("WF_SCHEMA_INVALID", `non-JSON value of type '${typeof value}'`);
}

export function digest(value) {
  return createHash("sha256").update(canonical(value), "utf8").digest("hex");
}

export function loadJson(file) {
  try {
    return JSON.parse(readFileSync(file, "utf8"));
  } catch (error) {
    throw fail("WF_SCHEMA_INVALID", `cannot read JSON: ${error.message}`, file);
  }
}

function jsonType(value) {
  if (value === null) return "null";
  if (Array.isArray(value)) return "array";
  if (typeof value === "number") return Number.isInteger(value) ? "integer" : "number";
  if (typeof value === "object") return "object";
  return typeof value;
}

function matchesType(value, declared) {
  const actual = jsonType(value);
  return actual === declared || (declared === "number" && actual === "integer");
}

function pointerGet(value, pointer) {
  if (!pointer) return value;
  let current = value;
  for (const encoded of pointer.slice(1).split("/")) {
    const part = encoded.replaceAll("~1", "/").replaceAll("~0", "~");
    if (Array.isArray(current) && /^\d+$/.test(part) && Number(part) < current.length) current = current[Number(part)];
    else if (current && typeof current === "object" && Object.hasOwn(current, part)) current = current[part];
    else return MISSING;
  }
  return current;
}

function referenceValue(reference, output, runInputs, inputs = {}) {
  const parts = reference.split(".");
  let current;
  let tail;
  if (parts[0] === "output" && parts.length >= 2) {
    current = Object.hasOwn(output, parts[1]) ? output[parts[1]] : MISSING;
    tail = parts.slice(2);
  } else if (parts[0] === "input" && parts.length >= 2) {
    current = Object.hasOwn(inputs, parts[1]) ? inputs[parts[1]] : MISSING;
    tail = parts.slice(2);
  } else if (parts[0] === "run" && parts[1] === "input" && parts.length >= 3) {
    current = Object.hasOwn(runInputs, parts[2]) ? runInputs[parts[2]] : MISSING;
    tail = parts.slice(3);
  } else return MISSING;
  for (const part of tail) {
    if (current && typeof current === "object" && Object.hasOwn(current, part)) current = current[part];
    else return MISSING;
  }
  return current;
}

function operandValue(operand, output, runInputs, inputs = {}) {
  if (!operand || typeof operand !== "object" || Array.isArray(operand)) {
    throw fail("RUN_PREDICATE_INVALID", "operand must be an object");
  }
  if (Object.keys(operand).length === 1 && Object.hasOwn(operand, "literal")) return operand.literal;
  if (Object.keys(operand).length === 1 && Object.hasOwn(operand, "ref")) {
    const value = referenceValue(operand.ref, output, runInputs, inputs);
    if (value === MISSING) throw fail("RUN_PREDICATE_INVALID", `predicate reference '${operand.ref}' is missing`);
    return value;
  }
  throw fail("RUN_PREDICATE_INVALID", "operand must contain exactly literal or ref");
}

export function evaluate(predicate, output, runInputs, inputs = {}) {
  if (!predicate || typeof predicate !== "object" || Array.isArray(predicate)) {
    throw fail("RUN_PREDICATE_INVALID", "predicate must be an object");
  }
  if (Object.keys(predicate).length === 1 && typeof predicate.const === "boolean") return predicate.const;
  const op = predicate.op;
  if (["eq", "ne", "lt", "lte", "gt", "gte"].includes(op)) {
    if (!Array.isArray(predicate.args) || predicate.args.length !== 2) {
      throw fail("RUN_PREDICATE_INVALID", `operator '${op}' requires two operands`);
    }
    const left = operandValue(predicate.args[0], output, runInputs, inputs);
    const right = operandValue(predicate.args[1], output, runInputs, inputs);
    if (op === "eq" || op === "ne") {
      const equal = jsonType(left) === jsonType(right) && canonical(left) === canonical(right);
      return op === "eq" ? equal : !equal;
    }
    const leftType = jsonType(left);
    const rightType = jsonType(right);
    const numeric = ["integer", "number"].includes(leftType) && ["integer", "number"].includes(rightType);
    if (!numeric && !(leftType === "string" && rightType === "string")) {
      throw fail("RUN_PREDICATE_INVALID", `operator '${op}' requires two numbers or two strings`);
    }
    if (op === "lt") return left < right;
    if (op === "lte") return left <= right;
    if (op === "gt") return left > right;
    return left >= right;
  }
  if (op === "present") {
    if (!predicate.arg || Object.keys(predicate.arg).length !== 1 || typeof predicate.arg.ref !== "string") {
      throw fail("RUN_PREDICATE_INVALID", "present requires one ref operand");
    }
    return referenceValue(predicate.arg.ref, output, runInputs, inputs) !== MISSING;
  }
  if (op === "all" || op === "any") {
    if (!Array.isArray(predicate.args) || predicate.args.length === 0) {
      throw fail("RUN_PREDICATE_INVALID", `operator '${op}' requires predicates`);
    }
    // Evaluate every operand in authored order so the selected error is stable.
    const values = predicate.args.map((item) => evaluate(item, output, runInputs, inputs));
    return op === "all" ? values.every(Boolean) : values.some(Boolean);
  }
  if (op === "not") return !evaluate(predicate.arg, output, runInputs, inputs);
  throw fail("RUN_PREDICATE_INVALID", `unknown predicate operator '${op}'`);
}

function checkPredicate(predicate, outputNames, runInputNames, location, inputNames = new Set()) {
  const errors = [];
  if (!predicate || typeof predicate !== "object" || Array.isArray(predicate)) {
    return [fail("WF_SCHEMA_INVALID", "predicate must be an object", location)];
  }
  if (Object.keys(predicate).length === 1 && typeof predicate.const === "boolean") return errors;
  const checkRef = (ref, where) => {
    if (typeof ref !== "string") errors.push(fail("WF_SCHEMA_INVALID", "predicate ref must be a string", where));
    else if (ref.startsWith("output.") && !outputNames.has(ref.split(".")[1])) errors.push(fail("WF_REFERENCE_MISSING", `unknown output reference '${ref}'`, where));
    else if (ref.startsWith("input.") && !inputNames.has(ref.split(".")[1])) errors.push(fail("WF_REFERENCE_MISSING", `unknown gate input reference '${ref}'`, where));
    else if (ref.startsWith("run.input.") && !runInputNames.has(ref.split(".")[2])) errors.push(fail("WF_REFERENCE_MISSING", `unknown run input reference '${ref}'`, where));
    else if (!ref.startsWith("output.") && !ref.startsWith("input.") && !ref.startsWith("run.input.")) errors.push(fail("WF_SCHEMA_INVALID", `invalid predicate reference '${ref}'`, where));
  };
  const op = predicate.op;
  if (["eq", "ne", "lt", "lte", "gt", "gte"].includes(op)) {
    if (!Array.isArray(predicate.args) || predicate.args.length !== 2) errors.push(fail("WF_SCHEMA_INVALID", `operator '${op}' requires two operands`, location));
    else predicate.args.forEach((arg, index) => {
      const where = `${location}/args/${index}`;
      if (!arg || typeof arg !== "object" || Array.isArray(arg) || !(["ref", "literal"].some((key) => Object.hasOwn(arg, key))) || Object.keys(arg).length !== 1) errors.push(fail("WF_SCHEMA_INVALID", "invalid operand", where));
      else if (Object.hasOwn(arg, "ref")) checkRef(arg.ref, `${where}/ref`);
    });
  } else if (op === "present") {
    if (!predicate.arg || Object.keys(predicate.arg).length !== 1 || !Object.hasOwn(predicate.arg, "ref")) errors.push(fail("WF_SCHEMA_INVALID", "present requires one ref", location));
    else checkRef(predicate.arg.ref, `${location}/arg/ref`);
  } else if (op === "all" || op === "any") {
    if (!Array.isArray(predicate.args) || predicate.args.length === 0) errors.push(fail("WF_SCHEMA_INVALID", `operator '${op}' requires predicates`, location));
    else predicate.args.forEach((arg, index) => errors.push(...checkPredicate(arg, outputNames, runInputNames, `${location}/args/${index}`, inputNames)));
  } else if (op === "not") errors.push(...checkPredicate(predicate.arg, outputNames, runInputNames, `${location}/arg`, inputNames));
  else errors.push(fail("WF_SCHEMA_INVALID", `unknown predicate operator '${op}'`, location));
  return errors;
}

function predicateReferences(predicate, prefix) {
  if (!predicate || typeof predicate !== "object") return false;
  if (typeof predicate.ref === "string" && predicate.ref.startsWith(prefix)) return true;
  return Object.values(predicate).some((value) => {
    if (Array.isArray(value)) return value.some((item) => predicateReferences(item, prefix));
    return value && typeof value === "object" && predicateReferences(value, prefix);
  });
}

export function validateWorkflow(document, sourcePath = null, callStack = []) {
  const errors = [];
  if (!document || typeof document !== "object" || Array.isArray(document)) return [fail("WF_SCHEMA_INVALID", "workflow must be an object")];
  if (document.apiVersion !== "darkstar.local/v1alpha1" || document.kind !== "Workflow") errors.push(fail("WF_SCHEMA_INVALID", "unsupported apiVersion or kind"));
  const metadata = document.metadata;
  const spec = document.spec;
  if (!metadata || !spec || typeof metadata !== "object" || typeof spec !== "object") return [...errors, fail("WF_SCHEMA_INVALID", "metadata and spec must be objects")];
  if (typeof metadata.name !== "string" || typeof metadata.version !== "string") errors.push(fail("WF_SCHEMA_INVALID", "metadata name and version are required", "/metadata"));
  const runInputs = spec.inputs ?? {};
  const nodes = spec.nodes;
  let defaults = spec.routeDefaults;
  if (!runInputs || typeof runInputs !== "object" || !nodes || typeof nodes !== "object" || Object.keys(nodes).length === 0) return [...errors, fail("WF_SCHEMA_INVALID", "spec.inputs and nonempty spec.nodes are required", "/spec")];
  if (!defaults || typeof defaults !== "object") {
    errors.push(fail("WF_SCHEMA_INVALID", "routeDefaults is required", "/spec/routeDefaults"));
    defaults = {};
  }
  for (const [name, declaration] of Object.entries(runInputs)) {
    if (!ID_RE.test(name) || !declaration || !VALUE_TYPES.has(declaration.type)) errors.push(fail("WF_SCHEMA_INVALID", `invalid run input '${name}'`, `/spec/inputs/${name}`));
  }

  const transitionIds = new Map();
  const incoming = Object.fromEntries(Object.keys(nodes).map((id) => [id, []]));
  const edges = [];
  const entries = new Set();
  const terminals = new Set();

  for (const [nodeId, node] of Object.entries(nodes)) {
    const location = `/spec/nodes/${nodeId}`;
    if (!ID_RE.test(nodeId) || !node || typeof node !== "object" || Array.isArray(node)) {
      errors.push(fail("WF_SCHEMA_INVALID", `invalid node '${nodeId}'`, location));
      continue;
    }
    const requiredExecutor = { reasoning: "reasoning", gate: "gate", command: "command", approval: "approval", subworkflow: "call" }[node.type];
    if (!NODE_TYPES.has(node.type)) errors.push(fail("WF_SCHEMA_INVALID", `invalid node type '${node.type}'`, `${location}/type`));
    else if (!Object.hasOwn(node, requiredExecutor)) errors.push(fail("WF_SCHEMA_INVALID", `node type '${node.type}' lacks executor declaration`, location));
    if (node.entry === true) entries.add(nodeId);
    else if (node.entry !== false) errors.push(fail("WF_SCHEMA_INVALID", "entry must be boolean", `${location}/entry`));
    if (node.terminal === true) terminals.add(nodeId);
    else if (node.terminal !== false) errors.push(fail("WF_SCHEMA_INVALID", "terminal must be boolean", `${location}/terminal`));
    if (!node.inputs || typeof node.inputs !== "object" || !node.outputs || typeof node.outputs !== "object") {
      errors.push(fail("WF_SCHEMA_INVALID", "inputs and outputs must be objects", location));
      continue;
    }
    for (const [outputName, declaration] of Object.entries(node.outputs)) {
      if (!ID_RE.test(outputName) || !declaration || !VALUE_TYPES.has(declaration.type)) errors.push(fail("WF_SCHEMA_INVALID", `invalid output '${outputName}'`, `${location}/outputs/${outputName}`));
    }
    const transitions = node.transitions ?? [];
    if (!Array.isArray(transitions)) {
      errors.push(fail("WF_SCHEMA_INVALID", "transitions must be an array", `${location}/transitions`));
      continue;
    }
    transitions.forEach((edge, index) => {
      const edgeLocation = `${location}/transitions/${index}`;
      if (!edge || typeof edge !== "object" || !ID_RE.test(String(edge.id ?? ""))) {
        errors.push(fail("WF_SCHEMA_INVALID", "transition needs a valid id", edgeLocation));
        return;
      }
      if (transitionIds.has(edge.id)) errors.push(fail("WF_SCHEMA_INVALID", `duplicate transition id '${edge.id}'`, `${edgeLocation}/id`));
      transitionIds.set(edge.id, { source: nodeId, edge, location: edgeLocation });
      if (!Object.hasOwn(nodes, edge.to)) errors.push(fail("WF_REFERENCE_MISSING", `transition '${edge.id}' targets unknown node '${edge.to}'`, `${edgeLocation}/to`));
      else {
        incoming[edge.to].push(edge.id);
        edges.push({ source: nodeId, target: edge.to, edge });
      }
      const kind = edge.kind ?? "normal";
      if (!["normal", "bounded"].includes(kind)) errors.push(fail("WF_SCHEMA_INVALID", `invalid transition kind '${kind}'`, `${edgeLocation}/kind`));
      if (kind === "bounded" && (!Number.isInteger(edge.maxTraversals) || edge.maxTraversals < 1)) errors.push(fail("WF_SCHEMA_INVALID", "bounded transition needs positive maxTraversals", edgeLocation));
      if (kind !== "bounded" && Object.hasOwn(edge, "maxTraversals")) errors.push(fail("WF_SCHEMA_INVALID", "normal transition cannot declare maxTraversals", edgeLocation));
      if (Object.hasOwn(edge, "when")) {
        errors.push(...checkPredicate(edge.when, new Set(Object.keys(node.outputs)), new Set(Object.keys(runInputs)), `${edgeLocation}/when`));
        if (node.type === "reasoning" && predicateReferences(edge.when, "output.")) {
          errors.push(fail("WF_REASONING_EDGE_INVALID", `reasoning node '${nodeId}' cannot branch on its own output`, `${edgeLocation}/when`));
        }
      }
    });
    if (node.type === "gate") {
      if (node.outputs.passed?.type !== "boolean" || node.outputs.gate_evidence?.type !== "object") {
        errors.push(fail("WF_GATE_INVALID", `gate '${nodeId}' must declare passed and gate_evidence outputs`, `${location}/outputs`));
      }
      if (!node.gate || typeof node.gate.policy !== "string" || !node.gate.condition) {
        errors.push(fail("WF_GATE_INVALID", `gate '${nodeId}' needs policy and condition`, `${location}/gate`));
      } else {
        errors.push(...checkPredicate(node.gate.condition, new Set(), new Set(Object.keys(runInputs)), `${location}/gate/condition`, new Set(Object.keys(node.inputs))));
        if (predicateReferences(node.gate.condition, "output.")) errors.push(fail("WF_GATE_INVALID", `gate '${nodeId}' condition cannot reference output`, `${location}/gate/condition`));
      }
    }
    if (node.checkpoint && typeof node.checkpoint === "object" && node.checkpoint.when) {
      errors.push(...checkPredicate(node.checkpoint.when, new Set(Object.keys(node.outputs)), new Set(Object.keys(runInputs)), `${location}/checkpoint/when`));
      if (node.type === "reasoning" && predicateReferences(node.checkpoint.when, "output.")) {
        errors.push(fail("WF_REASONING_EDGE_INVALID", `reasoning node '${nodeId}' cannot gate a checkpoint directly on its own output`, `${location}/checkpoint/when`));
      }
    }
  }

  for (const [nodeId, node] of Object.entries(nodes)) {
    if (!node || typeof node !== "object" || !node.inputs || typeof node.inputs !== "object") continue;
    for (const [bindingName, binding] of Object.entries(node.inputs)) {
      const location = `/spec/nodes/${nodeId}/inputs/${bindingName}`;
      if (!ID_RE.test(bindingName) || !binding || typeof binding !== "object") {
        errors.push(fail("WF_SCHEMA_INVALID", `invalid binding '${bindingName}'`, location));
        continue;
      }
      const source = binding.from;
      const targetType = binding.type;
      if (typeof source !== "string" || !VALUE_TYPES.has(targetType)) {
        errors.push(fail("WF_SCHEMA_INVALID", "binding needs source and type", location));
        continue;
      }
      const parts = source.split(".");
      let sourceType = null;
      if (parts.length === 3 && parts[0] === "run" && parts[1] === "input") sourceType = runInputs[parts[2]]?.type ?? null;
      else if (parts.length === 4 && parts[0] === "node" && parts[2] === "output") sourceType = nodes[parts[1]]?.outputs?.[parts[3]]?.type ?? null;
      if (!sourceType) errors.push(fail("WF_REFERENCE_MISSING", `unknown binding source '${source}'`, `${location}/from`));
      else if (!binding.pointer && sourceType !== targetType && !(sourceType === "integer" && targetType === "number")) errors.push(fail("WF_BINDING_INCOMPATIBLE", `binding '${bindingName}' expects ${targetType} from ${sourceType}`, location));
      if (Object.hasOwn(binding, "default") && !matchesType(binding.default, targetType)) errors.push(fail("WF_BINDING_INCOMPATIBLE", `default for '${bindingName}' has wrong type`, `${location}/default`));
    }
    const authoredIncoming = new Set(incoming[nodeId] ?? []);
    if (authoredIncoming.size > 1) {
      const declared = new Set(node.join?.from ?? []);
      const same = node.join && ["one", "all"].includes(node.join.mode) && authoredIncoming.size === declared.size && [...authoredIncoming].every((id) => declared.has(id));
      if (!same) errors.push(fail("WF_JOIN_INVALID", `node '${nodeId}' must join all authored incoming transitions`, `/spec/nodes/${nodeId}/join`, { transitionIds: [...authoredIncoming].sort() }));
    } else if (node.join !== undefined) errors.push(fail("WF_JOIN_INVALID", `node '${nodeId}' has a redundant join`, `/spec/nodes/${nodeId}/join`));
  }

  if (entries.size === 0) errors.push(fail("WF_SCHEMA_INVALID", "workflow has no entry-capable node", "/spec/nodes"));
  if (terminals.size === 0) errors.push(fail("WF_SCHEMA_INVALID", "workflow has no terminal-capable node", "/spec/nodes"));
  const adjacency = Object.fromEntries(Object.keys(nodes).map((id) => [id, new Set()]));
  const normalAdjacency = Object.fromEntries(Object.keys(nodes).map((id) => [id, new Set()]));
  for (const { source, target, edge } of edges) {
    adjacency[source].add(target);
    if ((edge.kind ?? "normal") !== "bounded") normalAdjacency[source].add(target);
  }
  const reachable = new Set();
  const stack = [...entries];
  while (stack.length) {
    const current = stack.pop();
    if (reachable.has(current)) continue;
    reachable.add(current);
    stack.push(...(adjacency[current] ?? []));
  }
  [...new Set(Object.keys(nodes).filter((id) => !reachable.has(id)))].sort().forEach((id) => errors.push(fail("WF_UNREACHABLE_NODE", `node '${id}' is unreachable`, `/spec/nodes/${id}`)));
  const colors = new Map();
  const visit = (id) => {
    colors.set(id, 1);
    for (const target of [...normalAdjacency[id]].sort()) {
      if (colors.get(target) === 1 || (!colors.has(target) && visit(target))) return true;
    }
    colors.set(id, 2);
    return false;
  };
  if (Object.keys(nodes).sort().some((id) => !colors.has(id) && visit(id))) errors.push(fail("WF_UNBOUNDED_CYCLE", "normal transitions contain a directed cycle", "/spec/nodes"));
  if (!entries.has(defaults.entry)) errors.push(fail("WF_DEFAULT_ROUTE_INVALID", "invalid default entry", "/spec/routeDefaults/entry"));
  if (!Array.isArray(defaults.terminals) || defaults.terminals.length === 0 || defaults.terminals.some((id) => !terminals.has(id))) errors.push(fail("WF_DEFAULT_ROUTE_INVALID", "invalid default terminals", "/spec/routeDefaults/terminals"));

  if (sourcePath) {
    const absolute = resolve(sourcePath);
    if (callStack.includes(absolute)) errors.push(fail("WF_SUBWORKFLOW_RECURSION", `recursive sub-workflow '${sourcePath}'`, "/spec/nodes"));
    else for (const [nodeId, node] of Object.entries(nodes)) {
      if (node.type !== "subworkflow") continue;
      const reference = node.call?.workflow;
      if (!reference?.path) continue;
      const childPath = resolve(dirname(absolute), reference.path);
      try {
        const child = loadJson(childPath);
        if (child.metadata?.name !== reference.name || child.metadata?.version !== reference.version) errors.push(fail("WF_REFERENCE_MISSING", `sub-workflow identity mismatch for '${reference.path}'`, `/spec/nodes/${nodeId}/call/workflow`));
        errors.push(...validateWorkflow(child, childPath, [...callStack, absolute]));
      } catch (error) {
        errors.push(error instanceof WorkflowError ? error : fail("WF_REFERENCE_MISSING", error.message, `/spec/nodes/${nodeId}/call/workflow/path`));
      }
    }
  }
  if (errors.length === 0) {
    try { freezeRoute(document, defaults.entry, defaults.terminals); }
    catch (error) { errors.push(fail("WF_DEFAULT_ROUTE_INVALID", error.message, error.location, error.details)); }
  }
  return errors.sort((a, b) => a.location.localeCompare(b.location) || a.code.localeCompare(b.code) || canonical(a.details).localeCompare(canonical(b.details)));
}

function isEdgeEnabled(edge, overrides) {
  return overrides?.has(edge.id) ? overrides.get(edge.id) : (edge.enabledByDefault ?? true);
}

export function freezeRoute(document, entry, terminals, overrides = new Map()) {
  const nodes = document.spec.nodes;
  if (!nodes[entry] || nodes[entry].entry !== true) throw fail("ROUTE_ENTRY_INVALID", `node '${entry}' is not an allowed entry`, "/route/entry");
  if (!Array.isArray(terminals) || terminals.length === 0 || terminals.some((id) => !nodes[id] || nodes[id].terminal !== true)) throw fail("ROUTE_TERMINAL_INVALID", "one or more terminals are not terminal-capable", "/route/terminals");
  const terminalSet = new Set(terminals);
  const reachable = new Set();
  const candidateEdges = [];
  const stack = [entry];
  while (stack.length) {
    const source = stack.pop();
    if (reachable.has(source)) continue;
    reachable.add(source);
    if (terminalSet.has(source)) continue;
    for (const edge of nodes[source].transitions ?? []) {
      if (!isEdgeEnabled(edge, overrides)) continue;
      candidateEdges.push({ source, edge });
      stack.push(edge.to);
    }
  }
  const missing = [...terminalSet].filter((id) => !reachable.has(id));
  if (missing.length) throw fail("ROUTE_TERMINAL_INVALID", "selected terminal is unreachable", "/route/terminals", { nodeIds: missing.sort() });
  const reverse = Object.fromEntries([...reachable].map((id) => [id, new Set()]));
  for (const { source, edge } of candidateEdges) if (reachable.has(edge.to)) reverse[edge.to].add(source);
  const canReach = new Set(terminalSet);
  const reverseStack = [...terminalSet];
  while (reverseStack.length) {
    const current = reverseStack.pop();
    for (const source of reverse[current] ?? []) if (!canReach.has(source)) {
      canReach.add(source);
      reverseStack.push(source);
    }
  }
  const stranded = [...reachable].filter((id) => !canReach.has(id));
  if (stranded.length) throw fail("ROUTE_PATH_INCOMPLETE", "enabled branch cannot reach a selected terminal", "/route", { nodeIds: stranded.sort() });
  const edgeMap = new Map();
  const incoming = Object.fromEntries([...canReach].map((id) => [id, []]));
  const outgoing = Object.fromEntries([...canReach].map((id) => [id, []]));
  for (const { source, edge } of candidateEdges) if (canReach.has(source) && canReach.has(edge.to)) {
    edgeMap.set(edge.id, { source, edge });
    incoming[edge.to].push(edge.id);
    outgoing[source].push(edge.id);
  }
  Object.values(incoming).forEach((ids) => ids.sort());
  return { entry, terminals: terminalSet, nodes: canReach, edges: edgeMap, incoming, outgoing };
}

export function applyPatchBeforeRun(document, patch, terminals) {
  if (patch?.apiVersion !== "darkstar.local/v1alpha1" || patch?.kind !== "RoutePatch") throw fail("WF_SCHEMA_INVALID", "invalid route patch kind");
  if (patch.spec?.expectedRouteRevision !== 0) throw fail("ROUTE_PATCH_CONFLICT", "reference runner route revision is 0", "/spec/expectedRouteRevision");
  const known = new Set(Object.values(document.spec.nodes).flatMap((node) => (node.transitions ?? []).map((edge) => edge.id)));
  const overrides = new Map();
  let selectedTerminals = [...terminals];
  (patch.spec.operations ?? []).forEach((operation, index) => {
    if (["enableTransition", "disableTransition"].includes(operation?.op)) {
      if (!known.has(operation.transitionId)) throw fail("WF_REFERENCE_MISSING", `unknown transition '${operation.transitionId}'`, `/spec/operations/${index}/transitionId`);
      overrides.set(operation.transitionId, operation.op === "enableTransition");
    } else if (operation?.op === "setTerminals" && Array.isArray(operation.nodes)) selectedTerminals = [...operation.nodes];
    else throw fail("WF_SCHEMA_INVALID", `invalid patch operation '${operation?.op}'`, `/spec/operations/${index}`);
  });
  return { overrides, terminals: selectedTerminals };
}

export class Runner {
  constructor(document, sourcePath, fixture, entry, terminals, overrides = new Map()) {
    this.document = document;
    this.sourcePath = resolve(sourcePath);
    this.fixture = fixture ?? {};
    this.runInputs = this.fixture.runInputs ?? {};
    this.route = freezeRoute(document, entry, terminals, overrides);
    this.outputs = {};
    this.visitCounts = new Map();
    this.resultIndexes = new Map();
    this.checkpointIndexes = new Map();
    this.loopCounts = new Map();
    this.tokens = new Map();
    this.queue = [entry];
    this.events = [];
    this.reached = new Set();
  }

  count(map, key, increment = 0) {
    const value = map.get(key) ?? 0;
    if (increment) map.set(key, value + increment);
    return value;
  }

  resolveBinding(binding) {
    const parts = binding.from.split(".");
    let value = parts[0] === "run" ? (Object.hasOwn(this.runInputs, parts[2]) ? this.runInputs[parts[2]] : MISSING) : (Object.hasOwn(this.outputs[parts[1]] ?? {}, parts[3]) ? this.outputs[parts[1]][parts[3]] : MISSING);
    if (value !== MISSING && binding.pointer) value = pointerGet(value, binding.pointer);
    if (value === MISSING && Object.hasOwn(binding, "default")) value = binding.default;
    return value;
  }

  snapshotInputs(nodeId, node) {
    const snapshot = {};
    const missing = [];
    for (const [name, binding] of Object.entries(node.inputs ?? {})) {
      const value = this.resolveBinding(binding);
      if (value === MISSING) {
        if (binding.required ?? true) missing.push(name);
        continue;
      }
      if (!matchesType(value, binding.type)) throw fail("RUN_INPUT_REQUIRED", `input '${name}' has the wrong type`, `/spec/nodes/${nodeId}/inputs/${name}`);
      snapshot[name] = value;
    }
    if (missing.length) throw fail("RUN_INPUT_REQUIRED", `node '${nodeId}' is missing required input`, `/spec/nodes/${nodeId}/inputs`, { inputNames: missing.sort() });
    return snapshot;
  }

  nextResult(nodeId) {
    const values = this.fixture.results?.[nodeId] ?? [];
    const index = this.count(this.resultIndexes, nodeId);
    if (!values[index] || typeof values[index] !== "object" || Array.isArray(values[index])) throw fail("RUN_OUTPUT_INVALID", `fixture has no result for node '${nodeId}'`, `/fixture/results/${nodeId}/${index}`);
    this.resultIndexes.set(nodeId, index + 1);
    return values[index];
  }

  validateOutput(nodeId, node, candidate) {
    const violations = [];
    for (const [name, declaration] of Object.entries(node.outputs ?? {})) {
      if (!Object.hasOwn(candidate, name) && (declaration.required ?? true)) violations.push(`missing:${name}`);
      else if (Object.hasOwn(candidate, name) && !matchesType(candidate[name], declaration.type)) violations.push(`type:${name}`);
    }
    for (const name of Object.keys(candidate)) if (!Object.hasOwn(node.outputs ?? {}, name)) violations.push(`undeclared:${name}`);
    if (violations.length) throw fail("RUN_OUTPUT_INVALID", `node '${nodeId}' produced invalid output`, `/spec/nodes/${nodeId}/outputs`, { violations: violations.sort() });
  }

  checkpointMode(checkpoint, candidate) {
    if (checkpoint === undefined) return { mode: "none", maximum: null };
    if (typeof checkpoint === "string") return { mode: checkpoint, maximum: null };
    if (checkpoint.mode === "approve_on_change" && !evaluate(checkpoint.when ?? { const: true }, candidate, this.runInputs)) return { mode: "none", maximum: checkpoint.maxRevisions ?? null };
    return { mode: checkpoint.mode ?? "none", maximum: checkpoint.maxRevisions ?? null };
  }

  applyCheckpoint(nodeId, node, initial) {
    let candidate = initial;
    let revisions = 0;
    while (true) {
      this.validateOutput(nodeId, node, candidate);
      const { mode, maximum } = this.checkpointMode(node.checkpoint, candidate);
      if (mode === "none") return candidate;
      const actions = this.fixture.checkpoints?.[nodeId] ?? [];
      const index = this.count(this.checkpointIndexes, nodeId);
      if (index >= actions.length) throw fail("RUN_CHECKPOINT_ACTION_INVALID", `node '${nodeId}' needs a checkpoint decision`, `/fixture/checkpoints/${nodeId}`);
      const action = actions[index];
      this.checkpointIndexes.set(nodeId, index + 1);
      const allowed = {
        approve: new Set(["approve", "request_changes", "reject"]),
        acknowledge: new Set(["acknowledge"]),
        external: new Set(["satisfied"]),
        approve_on_change: new Set(["approve", "request_changes", "reject"]),
      }[mode] ?? new Set();
      if (!allowed.has(action)) throw fail("RUN_CHECKPOINT_ACTION_INVALID", `action '${action}' is invalid for '${mode}'`, `/fixture/checkpoints/${nodeId}/${index}`);
      if (["approve", "acknowledge", "satisfied"].includes(action)) return candidate;
      if (action === "reject") throw fail("RUN_CHECKPOINT_ACTION_INVALID", `node '${nodeId}' was rejected`, `/fixture/checkpoints/${nodeId}/${index}`);
      revisions += 1;
      if (maximum !== null && revisions > maximum) throw fail("RUN_CHECKPOINT_ACTION_INVALID", `node '${nodeId}' exhausted checkpoint revisions`, `/spec/nodes/${nodeId}/checkpoint`);
      candidate = this.nextResult(nodeId);
    }
  }

  runGate(nodeId, node, inputs) {
    const passed = evaluate(node.gate.condition, {}, this.runInputs, inputs);
    const candidate = {
      passed,
      gate_evidence: {
        policy: node.gate.policy,
        conditionDigest: digest(node.gate.condition),
        inputSnapshotDigest: digest(inputs),
        result: passed,
      },
    };
    this.events.push({
      type: "gate.evaluated",
      node: nodeId,
      policy: node.gate.policy,
      passed,
      conditionDigest: candidate.gate_evidence.conditionDigest,
      inputSnapshotDigest: candidate.gate_evidence.inputSnapshotDigest,
    });
    return candidate;
  }

  runSubworkflow(nodeId, node, inputs) {
    const call = node.call;
    const reference = call.workflow;
    const childPath = resolve(dirname(this.sourcePath), reference.path);
    const childDocument = loadJson(childPath);
    const childFixture = structuredClone(this.fixture.subworkflows?.[nodeId] ?? {});
    childFixture.runInputs = Object.fromEntries(Object.entries(call.inputs ?? {}).map(([childName, parentName]) => [childName, inputs[parentName]]));
    const child = new Runner(childDocument, childPath, childFixture, call.entry, call.terminals);
    child.run();
    const result = {};
    for (const [outputName, source] of Object.entries(call.outputs ?? {})) {
      const parts = source.split(".");
      const value = this.outputsFrom(child, parts[1], parts[3]);
      if (value === MISSING) throw fail("RUN_OUTPUT_INVALID", `child output '${source}' is missing`, `/spec/nodes/${nodeId}/call/outputs/${outputName}`);
      result[outputName] = value;
    }
    this.events.push({ type: "subworkflow.completed", node: nodeId, child: reference.name });
    return result;
  }

  outputsFrom(runner, nodeId, outputName) {
    return Object.hasOwn(runner.outputs[nodeId] ?? {}, outputName) ? runner.outputs[nodeId][outputName] : MISSING;
  }

  tokenCounter(nodeId) {
    if (!this.tokens.has(nodeId)) this.tokens.set(nodeId, new Map());
    return this.tokens.get(nodeId);
  }

  sendToken(edgeId) {
    const { edge } = this.route.edges.get(edgeId);
    const target = edge.to;
    const incoming = this.route.incoming[target];
    if (incoming.length <= 1) {
      this.queue.push(target);
      return;
    }
    const tokens = this.tokenCounter(target);
    tokens.set(edgeId, (tokens.get(edgeId) ?? 0) + 1);
    const node = this.document.spec.nodes[target];
    const required = node.join.from.filter((id) => incoming.includes(id));
    if (node.join.mode === "all" && required.every((id) => (tokens.get(id) ?? 0) > 0)) {
      required.forEach((id) => tokens.set(id, tokens.get(id) - 1));
      this.queue.push(target);
    }
  }

  resolveClosedJoins() {
    let scheduled = false;
    for (const nodeId of [...this.tokens.keys()].sort()) {
      const tokens = this.tokens.get(nodeId);
      const tokenCount = [...tokens.values()].reduce((sum, value) => sum + value, 0);
      if (tokenCount === 0) continue;
      const node = this.document.spec.nodes[nodeId];
      if (node.join.mode === "one") {
        if (tokenCount > 1) throw fail("RUN_JOIN_MULTIPLE", `join '${nodeId}' received multiple tokens`, `/spec/nodes/${nodeId}/join`, { transitionIds: [...tokens].filter(([, count]) => count).map(([id]) => id).sort() });
        const edgeId = [...tokens].find(([, count]) => count)[0];
        tokens.set(edgeId, tokens.get(edgeId) - 1);
        this.queue.push(nodeId);
        scheduled = true;
      } else {
        const required = node.join.from.filter((id) => this.route.incoming[nodeId].includes(id));
        if (!required.every((id) => (tokens.get(id) ?? 0) > 0)) throw fail("RUN_JOIN_UNSATISFIABLE", `join '${nodeId}' cannot receive all tokens`, `/spec/nodes/${nodeId}/join`);
      }
    }
    return scheduled;
  }

  execute(nodeId) {
    const node = this.document.spec.nodes[nodeId];
    const visit = this.count(this.visitCounts, nodeId) + 1;
    this.visitCounts.set(nodeId, visit);
    const inputs = this.snapshotInputs(nodeId, node);
    this.events.push({ type: "node.started", node: nodeId, visit });
    let candidate;
    if (node.type === "subworkflow") candidate = this.runSubworkflow(nodeId, node, inputs);
    else if (node.type === "gate") candidate = this.runGate(nodeId, node, inputs);
    else candidate = this.nextResult(nodeId);
    candidate = this.applyCheckpoint(nodeId, node, candidate);
    this.outputs[nodeId] = candidate;
    this.events.push({ type: "node.succeeded", node: nodeId, visit });
    if (this.route.terminals.has(nodeId)) {
      this.reached.add(nodeId);
      this.events.push({ type: "terminal.reached", node: nodeId });
      return;
    }
    const matches = [];
    for (const edgeId of this.route.outgoing[nodeId] ?? []) {
      const { edge } = this.route.edges.get(edgeId);
      if (!evaluate(edge.when ?? { const: true }, candidate, this.runInputs)) continue;
      if ((edge.kind ?? "normal") === "bounded" && this.count(this.loopCounts, edgeId) >= edge.maxTraversals) throw fail("RUN_LOOP_LIMIT_EXHAUSTED", `transition '${edgeId}' exhausted its traversal budget`, `/spec/nodes/${nodeId}/transitions`, { transitionId: edgeId });
      matches.push(edgeId);
    }
    if (matches.length === 0) throw fail("RUN_EDGE_NO_MATCH", `node '${nodeId}' matched no transition`, `/spec/nodes/${nodeId}/transitions`);
    const mode = node.transitionMode ?? "exclusive";
    if (mode === "exclusive" && matches.length !== 1) throw fail("RUN_EDGE_AMBIGUOUS", `node '${nodeId}' matched ${matches.length} exclusive transitions`, `/spec/nodes/${nodeId}/transitions`, { transitionIds: [...matches].sort() });
    for (const edgeId of mode === "fanout" ? matches : matches.slice(0, 1)) {
      const { edge } = this.route.edges.get(edgeId);
      if ((edge.kind ?? "normal") === "bounded") this.loopCounts.set(edgeId, this.count(this.loopCounts, edgeId) + 1);
      this.events.push({ type: "transition.fired", transition: edgeId, from: nodeId, to: edge.to });
      this.sendToken(edgeId);
    }
  }

  run() {
    while (true) {
      while (this.queue.length) this.execute(this.queue.shift());
      if (!this.resolveClosedJoins()) break;
    }
    if (this.reached.size === 0) throw fail("RUN_DEAD_END", "execution closed without reaching a selected terminal", "/route");
    return {
      status: "completed",
      workflow: this.document.metadata.name,
      workflowDigest: digest(this.document),
      entry: this.route.entry,
      terminalsReached: [...this.reached].sort(),
      visits: Object.fromEntries([...this.visitCounts].sort()),
      loopTraversals: Object.fromEntries([...this.loopCounts].sort()),
      outputs: this.outputs,
      events: this.events,
    };
  }
}

function parseArguments(argv) {
  const [command, ...rest] = argv;
  if (!command) return { command: "help" };
  if (command === "validate") return { command, paths: rest };
  if (command !== "run") return { command: "help" };
  const options = { command, workflow: rest.shift(), terminals: [] };
  while (rest.length) {
    const flag = rest.shift();
    const value = rest.shift();
    if (flag === "--fixture") options.fixture = value;
    else if (flag === "--entry") options.entry = value;
    else if (flag === "--terminal") options.terminals.push(value);
    else if (flag === "--patch") options.patch = value;
    else throw fail("WF_SCHEMA_INVALID", `unknown argument '${flag}'`);
  }
  return options;
}

function print(value) {
  process.stdout.write(`${JSON.stringify(value, null, 2)}\n`);
}

export function main(argv = process.argv.slice(2)) {
  const args = parseArguments(argv);
  try {
    if (args.command === "validate") {
      let failed = false;
      const results = args.paths.map((item) => {
        const file = resolve(item);
        try {
          const document = loadJson(file);
          const errors = validateWorkflow(document, file);
          if (errors.length) failed = true;
          return { path: item, valid: errors.length === 0, digest: digest(document), errors: errors.map((error) => error.toJSON()) };
        } catch (error) {
          failed = true;
          return { path: item, valid: false, errors: [(error instanceof WorkflowError ? error : fail("WF_SCHEMA_INVALID", error.message)).toJSON()] };
        }
      });
      print(results);
      return failed ? 1 : 0;
    }
    if (args.command === "run" && args.workflow) {
      const file = resolve(args.workflow);
      const document = loadJson(file);
      const errors = validateWorkflow(document, file);
      if (errors.length) {
        print({ status: "invalid", errors: errors.map((error) => error.toJSON()) });
        return 1;
      }
      const fixture = args.fixture ? loadJson(resolve(args.fixture)) : {};
      const defaults = document.spec.routeDefaults;
      let terminals = args.terminals.length ? args.terminals : [...defaults.terminals];
      let overrides = new Map();
      if (args.patch) ({ overrides, terminals } = applyPatchBeforeRun(document, loadJson(resolve(args.patch)), terminals));
      const runner = new Runner(document, file, fixture, args.entry ?? defaults.entry, terminals, overrides);
      print(runner.run());
      return 0;
    }
    process.stderr.write("Usage:\n  node scripts/workflow-reference.mjs validate <workflow...>\n  node scripts/workflow-reference.mjs run <workflow> [--fixture file] [--entry node] [--terminal node] [--patch file]\n");
    return 2;
  } catch (error) {
    const normalized = error instanceof WorkflowError ? error : fail("RUN_INVARIANT_VIOLATION", error.message);
    print({ status: "failed", error: normalized.toJSON() });
    return 1;
  }
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) process.exitCode = main();
