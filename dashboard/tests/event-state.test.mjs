import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  EventSequenceError,
  advanceEventCursor,
  openEventStream,
  parseDomainEvent,
  parseServerSentEvents,
  replayRecovery,
  reconnectDelay,
} from "../src/state/events.ts";

const encoder = new TextEncoder();

async function* chunks(...values) {
  for (const value of values) {
    yield typeof value === "string" ? value : encoder.encode(value.text);
  }
}

function domainEvent(globalPosition, kind = "run.updated") {
  const suffix = String(globalPosition).padStart(26, "0");
  return {
    schemaVersion: 1,
    id: `event_${suffix}`,
    globalPosition,
    aggregateType: "run",
    aggregateId: `run_${suffix}`,
    aggregateRevision: globalPosition,
    kind,
    occurredAt: "2026-01-01T00:00:00Z",
    recordedAt: "2026-01-01T00:00:00Z",
    correlationId: "corr_test",
    causationId: null,
    commandId: "command_test",
    actor: { type: "system", id: "darkstar" },
    data: { status: "running" },
    metadata: {},
  };
}

test("SSE parsing preserves order across chunk boundaries and ignores keepalives", async () => {
  const fourth = domainEvent(4, "run.started");
  const fifth = domainEvent(5, "attempt.started");
  const source = chunks(
    ": keepalive\r\nid: 4\r\nevent: run.started\r\nda",
    { text: `ta: ${JSON.stringify(fourth)}\r\n\r\nid: 5\nevent: attempt.started\nd` },
    `ata: ${JSON.stringify(fifth)}\n\n`,
  );

  const messages = [];
  for await (const message of parseServerSentEvents(source)) messages.push(message);

  assert.deepEqual(messages.map(({ id, event }) => ({ id, event })), [
    { id: "4", event: "run.started" },
    { id: "5", event: "attempt.started" },
  ]);
  assert.deepEqual(messages.map(parseDomainEvent), [fourth, fifth]);
});

test("domain event parsing rejects an SSE id that disagrees with its durable position", () => {
  const event = domainEvent(9);

  assert.throws(
    () => parseDomainEvent({ id: "8", event: event.kind, data: JSON.stringify(event) }),
    (error) => error instanceof EventSequenceError && error.expected === 9 && error.received === 8,
  );
});

test("cursor advancement deduplicates replayed events and fails closed on a gap", () => {
  assert.deepEqual(advanceEventCursor(12, domainEvent(12)), { cursor: 12, accepted: false });
  assert.deepEqual(advanceEventCursor(12, domainEvent(13)), { cursor: 13, accepted: true });
  assert.throws(
    () => advanceEventCursor(13, domainEvent(15)),
    (error) => error instanceof EventSequenceError && error.expected === 14 && error.received === 15,
  );
});

test("event stream resumes with the in-memory bearer token and last committed cursor", async () => {
  const calls = [];
  const response = await openEventStream({
    authorization: "Bearer dashboard-test-token",
    cursor: 41,
    fetcher: async (input, init) => {
      calls.push({ input, init });
      return new Response(": keepalive\n\n", {
        status: 200,
        headers: { "Content-Type": "text/event-stream" },
      });
    },
  });

  assert.equal(response.status, 200);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].input, "/api/v1/events");
  assert.equal(calls[0].init.method, "GET");
  assert.equal(new Headers(calls[0].init.headers).get("Authorization"), "Bearer dashboard-test-token");
  assert.equal(new Headers(calls[0].init.headers).get("Last-Event-ID"), "41");
});

test("expired replay history requires authoritative rehydration at the advertised boundary", async () => {
  const failure = await openEventStream({
    authorization: "Bearer dashboard-test-token",
    cursor: 2,
    fetcher: async () => new Response(JSON.stringify({
      schemaVersion: 1,
      code: "EVENT_REPLAY_UNAVAILABLE",
      message: "The requested event position is outside retained online history.",
      requestId: "request_test",
      retryable: false,
      details: [
        { field: "oldestAvailablePosition", code: "MINIMUM", message: "5" },
        { field: "resync", code: "LINK", message: "/api/v1/" },
      ],
    }), {
      status: 410,
      headers: { "Content-Type": "application/json" },
    }),
  }).then(
    () => assert.fail("expected replay history failure"),
    (error) => error,
  );

  assert.deepEqual(replayRecovery(failure), { rehydrate: true, cursor: 4 });
  assert.equal(replayRecovery(new Error("network unavailable")), undefined);
});

test("reconnect delay is deterministic under injected jitter, nondecreasing, and capped", () => {
  const delays = Array.from({ length: 12 }, (_, attempt) => reconnectDelay(attempt, () => 0.5));

  assert.deepEqual(delays.slice(0, 6), [500, 1_000, 2_000, 4_000, 8_000, 15_000]);
  assert.ok(delays.every((delay, index) => index === 0 || delay >= delays[index - 1]));
  assert.ok(delays.slice(5).every((delay) => delay === 15_000), "backoff should remain capped");
});

test("the provider rebases replay only after an authoritative refresh succeeds", async () => {
  const provider = await readFile(new URL("../src/state/DashboardStateProvider.tsx", import.meta.url), "utf8");
  const recoveryBranch = provider.slice(
    provider.indexOf("if (recovery)"),
    provider.indexOf("} else if (isNewerCursor(error))"),
  );

  assert.match(recoveryBranch, /await refresh\(\);\s*synchronized = true/);
  assert.match(recoveryBranch, /if \(synchronized\)[\s\S]*cursor\.current = recovery\.cursor/);
});

test("ordered events invalidate projections without inventing resource state", async () => {
  const reducer = await readFile(new URL("../src/state/dashboardState.ts", import.meta.url), "utf8");
  const eventBranch = reducer.slice(reducer.indexOf('case "event"'), reducer.indexOf("\n  }\n}"));

  assert.match(eventBranch, /advanceEventCursor\(state\.cursor, action\.event\)/);
  assert.doesNotMatch(eventBranch, /snapshot\s*:/);
});
