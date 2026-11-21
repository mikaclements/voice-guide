// The Agents tab's form: one field per agent file setting, as text the developer types, and the
// mapping between it and the settings voice answers and the dev server writes.
import type { AgentSettings } from "./agents.ts";

export type AgentForm = {
  id: string;
  name: string;
  description: string;
  // Lists, comma-separated.
  reads: string;
  sessionContext: string;
  summaryInstruction: string;
  summaryModel: string;
  summaryDocuments: string;
  // A JSON Schema object, as JSON; empty for none.
  summarySchema: string;
  opens: "agent" | "user";
  // Empty for the server's.
  model: string;
  voice: string;
  speed: string;
  persona: string;
};

const joined = (list: string[]) => list.join(", ");
const listed = (text: string) =>
  text
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);

export function formFrom(a: AgentSettings): AgentForm {
  return {
    id: a.id,
    name: a.name,
    description: a.description,
    reads: joined(a.reads),
    sessionContext: joined(a.session_context),
    summaryInstruction: a.summary?.instruction ?? "",
    summaryModel: a.summary?.model ?? "",
    summaryDocuments: joined(a.summary?.documents ?? []),
    summarySchema: a.summary?.schema ? JSON.stringify(a.summary.schema, null, 2) : "",
    opens: a.opens,
    model: a.model ?? "",
    voice: a.voice ?? "",
    speed: a.speed === null ? "" : String(a.speed),
    persona: a.persona,
  };
}

// The settings the form holds, or why JSON can't carry them. Everything else is voice's to check
// when it reads the file, so its problem says what is wrong in the loader's own words.
export function settingsFrom(f: AgentForm): AgentSettings | string {
  let schema: Record<string, unknown> | null = null;
  if (f.summarySchema.trim()) {
    try {
      schema = JSON.parse(f.summarySchema);
    } catch {
      schema = null;
    }
    if (schema === null || typeof schema !== "object" || Array.isArray(schema)) {
      return "The summary schema must be a JSON object, or empty for none.";
    }
  }
  const speed = f.speed.trim() ? Number(f.speed) : null;
  if (speed !== null && !Number.isFinite(speed)) {
    return "Speed must be a number, or empty for the server's.";
  }
  const documents = listed(f.summaryDocuments);
  const summary =
    f.summaryInstruction || f.summaryModel || documents.length || schema
      ? { instruction: f.summaryInstruction, model: f.summaryModel || null, schema, documents }
      : null;
  return {
    id: f.id,
    name: f.name,
    description: f.description,
    reads: listed(f.reads),
    session_context: listed(f.sessionContext),
    summary,
    opens: f.opens,
    model: f.model || null,
    voice: f.voice || null,
    speed,
    persona: f.persona,
  };
}

// A new agent: a fresh id, and every setting left to the loader's default.
export function newForm(): AgentForm {
  return formFrom({
    id: uuidv7(),
    name: "",
    description: "",
    reads: [],
    session_context: [],
    summary: null,
    opens: "user",
    model: null,
    voice: null,
    speed: null,
    persona: "",
  });
}

// A test agent copied from another, usually a shipped one the page only reads: its own id, so
// publishing it never replaces the original.
export function copyForm(f: AgentForm): AgentForm {
  return { ...f, id: uuidv7(), name: `${f.name} (test)` };
}

// A UUID version 7 (RFC 9562): the time in milliseconds, then random bits, so agents' IDs sort by
// when they were made, as every other ID in voice-guide does.
export function uuidv7(now = Date.now()): string {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  for (let i = 0; i < 6; i++) bytes[i] = Math.floor(now / 2 ** (8 * (5 - i))) % 256;
  bytes[6] = (bytes[6] & 0x0f) | 0x70;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
  return hex.replace(/^(.{8})(.{4})(.{4})(.{4})/, "$1-$2-$3-$4-");
}
