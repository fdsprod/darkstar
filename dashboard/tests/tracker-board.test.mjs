import assert from "node:assert/strict";
import test from "node:test";

import { executionLabel, ticketExecutions, trackerColumnFor, trackerDropActions, UNMAPPED_COLUMN } from "../src/pages/trackerBoardModel.ts";

const columns = [
  { id: "planning", name: "Planning", statusIds: ["state-a", "state-b"] },
  { id: "delivery", name: "Delivery", statusIds: ["state-c"] },
];
const ticket = { ticketKey: "stable", bindingRevision: 2, currentSource: true, freshness: "fresh", status: "fresh", businessState: { state: "known", value: { id: "state-b", name: "Custom triage" } } };
const action = { id: "ship", targetStateId: "state-c", name: "Submit for acceptance", availability: "available", reason: "", automation: ["Automatic intake starts the configured workflow"] };

test("tracker grouping uses stable source IDs, supports many to one and never mutates tickets", () => {
  const original = structuredClone(ticket);
  assert.equal(trackerColumnFor(ticket, columns), "planning");
  assert.deepEqual(ticket, original);
  assert.equal(trackerColumnFor({ ...ticket, businessState: { state: "known", value: { id: "state-new", name: "Planning" } } }, columns), UNMAPPED_COLUMN);
  assert.equal(trackerColumnFor({ ...ticket, businessState: { state: "unknown", reason: "offline" } }, columns), UNMAPPED_COLUMN);
  assert.equal(trackerColumnFor({ ...ticket, currentSource: false }, columns), UNMAPPED_COLUMN);
  assert.equal(trackerColumnFor({ ...ticket, currentSource: false }, columns, "source-history"), "source-history");
  assert.equal(trackerColumnFor(ticket, [...columns, { id: "overlap", name: "Conflict", statusIds: ["state-b"] }]), UNMAPPED_COLUMN);
});

test("only a fresh current source ticket can request a discovered source transition", () => {
  assert.deepEqual(trackerDropActions(ticket, columns[1], [action]), [action]);
  assert.deepEqual(trackerDropActions({ ...ticket, status: "cached" }, columns[1], [action]), [action]);
  for (const override of [{ currentSource: false }, { freshness: "stale" }, { status: "inaccessible" }]) {
    assert.deepEqual(trackerDropActions({ ...ticket, ...override }, columns[1], [action]), []);
  }
  assert.deepEqual(trackerDropActions(ticket, columns[0], [action]), []);
  assert.deepEqual(trackerDropActions(ticket, columns[1], [{ ...action, availability: "unavailable" }]), []);
});

test("execution completion and external status stay independent, including a later waiting run", () => {
  assert.equal(executionLabel({ localActivity: "idle", runOutcome: "completed" }), "Workflow complete");
  assert.equal(executionLabel({ localActivity: "waiting", runOutcome: "completed" }), "waiting");
  const current = { lineage: { ticketKey: "stable", bindingRevision: 2 } };
  const previous = { lineage: { ticketKey: "stable", bindingRevision: 1 } };
  assert.deepEqual(ticketExecutions(ticket, [current, previous]), [current]);
});
