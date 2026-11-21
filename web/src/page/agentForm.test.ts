import assert from "node:assert/strict";
import { test } from "node:test";
import {
  cloneForm,
  droppedDocuments,
  formFrom,
  modelChoice,
  models,
  newForm,
  settingsFrom,
  uuidv7,
} from "./agentForm.ts";
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

test("a clone of an agent is the same settings under a fresh UUIDv7, its name marked as a copy", () => {
  const clone = cloneForm(formFrom(full));
  assert.match(clone.id, uuidV7);
  assert.deepEqual(settingsFrom(clone), { ...full, id: clone.id, name: 'Keeper: "the" #1 (copy)' });
});

test("the summary's documents are checked among the session context names, and the others it reads are listed apart, as only it reads them", () => {
  const agent = {
    ...full,
    session_context: ["resume", "job"],
    summary: {
      ...(full.summary as NonNullable<AgentSettings["summary"]>),
      documents: ["job", "ship_log"],
    },
  };
  const form = formFrom(agent);
  assert.deepEqual([form.summaryDocuments, form.summaryOnly], [["job"], "ship_log"]);
  const checked = settingsFrom({ ...form, summaryDocuments: ["job", "resume"] });
  assert.deepEqual(typeof checked === "object" && checked.summary?.documents, [
    "job",
    "resume",
    "ship_log",
  ]);
});

test("a checked document no longer among the session context names is dropped, and said to be", () => {
  const form = { ...formFrom(full), sessionContext: "job", summaryDocuments: ["job", "resume"] };
  assert.deepEqual(droppedDocuments(form), ["resume"]);
  const sent = settingsFrom(form);
  assert.deepEqual(typeof sent === "object" && sent.summary?.documents, ["job", "ship_log"]);
});

test("a model is picked from the list, empty for the default, or Other for any other name", () => {
  assert.deepEqual(models, [
    "claude-fable-5-2",
    "claude-opus-5-5",
    "claude-sonnet-5-5",
    "claude-haiku-5-5",
  ]);
  assert.deepEqual(
    [
      modelChoice("claude-opus-5-5", false),
      modelChoice("", false),
      modelChoice("my-model-1", false),
    ],
    ["claude-opus-5-5", "", "other"],
  );
  // Other chosen shows the name's field whatever the name, until another choice is made.
  assert.deepEqual(
    [modelChoice("", true), modelChoice("claude-opus-5-5", true)],
    ["other", "other"],
  );
});

test("the speed is a number from its field, or empty for the server's", () => {
  for (const [typed, speed] of [
    ["1.25", 1.25],
    ["0.5", 0.5],
    ["", null],
  ] as const) {
    const sent = settingsFrom({ ...formFrom(bare), speed: typed });
    assert.equal(typeof sent === "object" && sent.speed, speed, typed);
  }
});

test("a UUIDv7 holds its time in milliseconds, then version 7 and the RFC 9562 variant around random bits", () => {
  assert.match(
    uuidv7(0x0192_3456_789a),
    /^01923456-789a-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
  );
  assert.notEqual(uuidv7(0), uuidv7(0));
});
