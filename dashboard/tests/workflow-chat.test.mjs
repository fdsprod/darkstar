import test from "node:test";
import assert from "node:assert/strict";
import { readChatStream } from "../src/pages/workflowChatModel.ts";

const stream = chunks => new ReadableStream({ start(controller) { for (const chunk of chunks) controller.enqueue(chunk); controller.close(); } });
test("chat streams split UTF-8, questions, and saved revisions in order", async () => {
  const values = [{ kind: "text", payload: { text: "Review → approve" } }, { kind: "draft", payload: { revision: 2 } }, { kind: "question", payload: { question: "Which route?", options: ["Approval"] } }, { kind: "done", payload: { message: "Complete" } }];
  const bytes = new TextEncoder().encode(values.map(value => JSON.stringify(value)).join("\n"));
  const received = [];
  await readChatStream(stream([...bytes].map(byte => Uint8Array.of(byte))), event => received.push(event));
  assert.deepEqual(received, values);
});
test("disconnects cannot appear as successful chat completion", async () => {
  await assert.rejects(readChatStream(stream([new TextEncoder().encode('{"kind":"text","payload":{"text":"working"}}\n')]), () => {}), /disconnected/);
});
test("unknown events fail closed", async () => {
  await assert.rejects(readChatStream(stream([new TextEncoder().encode('{"kind":"publish","payload":{}}\n')]), () => {}), /Unsupported/);
});
