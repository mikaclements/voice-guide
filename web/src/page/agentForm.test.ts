import assert from "node:assert/strict";
import { test } from "node:test";
import { copyForm, formFrom, newForm, settingsFrom, uuidv7 } from "./agentForm.ts";
import type { AgentSettings } from "./agents.ts";

const id = "01900000-0000-7000-8000-000000000001";
const uuidV7 = /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

// An agent with every setting set, as voice answers it.
const full: AgentSettings = {
  id,
  name: 'Keeper: "the" #1',
  description: "Line one\nline two",
  reads: ["profile", "session_summary"],
  session_context: ["resume"],
  summary: {
    instruction: "Note the ships.",
    model: "claude-opus-5-5",
    schema: { properties: { score: { type: "integer" } }, type: "object" },
    documents: ["ship_log"],
  },
  opens: "agent",
  model: "claude-sonnet-5-5",
  voice: "bf_emma",
  speed: 1.1,
  persona: "You answer.\n---\nStill the persona.\n",
};

// The same agent with every setting left to the server's or the loader's default.
const bare: AgentSettings = {
  id,
  name: "Keeper",
  description: "",
  reads: [],
  session_context: [],
  summary: null,
  opens: "user",
  model: null,
  voice: null,
  speed: null,
  persona: "",
};

// The summary as an instruction alone, the way most agent files write it.
const instructionOnly: AgentSettings = {
  ...bare,
  summary: { instruction: "Note the ships.", model: null, schema: null, documents: [] },
};

test("an agent filled into the form comes back from it as the same settings", () => {
  for (const agent of [full, bare, instructionOnly]) {
    assert.deepEqual(settingsFrom(formFrom(agent)), agent, agent.name);
  }
});

test("the form shows lists comma-separated and the summary schema as indented JSON, and reads lists back however they are spaced", () => {
  const form = formFrom(full);
  assert.equal(form.reads, "profile, session_summary");
  assert.equal(form.summarySchema, JSON.stringify(full.summary?.schema, null, 2));
  assert.equal(form.speed, "1.1");
  const spaced = settingsFrom({ ...form, reads: " profile ,,session_summary, " });
  assert.deepEqual(typeof spaced === "object" && spaced.reads, ["profile", "session_summary"]);
});

test("a summary with only a model, documents or a schema is still sent, for voice to say it needs an instruction", () => {
  const sent = settingsFrom({ ...formFrom(bare), summaryModel: "claude-opus-5-5" });
  assert.deepEqual(typeof sent === "object" && sent.summary, {
    instruction: "",
    model: "claude-opus-5-5",
    schema: null,
    documents: [],
  });
});

test("a form JSON can't carry is refused with why: a schema that isn't a JSON object, or a speed that isn't a number", () => {
  const form = formFrom(bare);
  for (const schema of ["{", "[1]", "null", '"object"']) {
    assert.match(String(settingsFrom({ ...form, summarySchema: schema })), /JSON object/, schema);
  }
  assert.match(String(settingsFrom({ ...form, speed: "fast" })), /Speed must be a number/);
});

test("a new agent is an empty form with a fresh UUIDv7, the user speaking first and the rest the server's", () => {
  const form = newForm();
  assert.match(form.id, uuidV7);
  assert.deepEqual(settingsFrom(form), { ...bare, id: form.id, name: "" });
});

test("a copy of an agent is the same settings under a fresh UUIDv7, its name marked as a test", () => {
  const copy = copyForm(formFrom(full));
  assert.match(copy.id, uuidV7);
  assert.deepEqual(settingsFrom(copy), { ...full, id: copy.id, name: 'Keeper: "the" #1 (test)' });
});

test("a UUIDv7 holds its time in milliseconds, then version 7 and the RFC 9562 variant around random bits", () => {
  assert.match(
    uuidv7(0x0192_3456_789a),
    /^01923456-789a-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
  );
  assert.notEqual(uuidv7(0), uuidv7(0));
});
