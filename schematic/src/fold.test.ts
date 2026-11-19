// Folding a level: each adapter hides behind the card it plugs into until that card is opened,
// and an outside card only adapters reach hides with them.
import assert from "node:assert/strict";
import { test } from "node:test";
import { foldedBehind, hiddenNodes, hostGroup } from "./fold.ts";
import type { View } from "./types.ts";

// A core with a service and two ports; a store implementing both, a fake implementing one, an
// inbound adapter calling the service, an outbound one with no port, and the outside cards.
const view: View = {
  id: "m",
  title: "m",
  level: 3,
  lead: "",
  considerations: [],
  nodes: [
    { id: "service", kind: "core", name: "Service" },
    { id: "p-a", kind: "port", name: "A" },
    { id: "p-b", kind: "port", name: "B" },
    { id: "store", kind: "adapter", name: "store" },
    { id: "fake", kind: "adapter", name: "fake" },
    { id: "http", kind: "adapter", name: "http" },
    { id: "files", kind: "adapter", name: "files" },
    { id: "x-db", kind: "store", name: "db", context: true },
    { id: "x-api", kind: "shared", name: "api", context: true },
    { id: "x-caller", kind: "module", name: "caller", context: true },
  ],
  edges: [
    { id: "1", from: "store", to: "p-a", style: "implements" },
    { id: "2", from: "store", to: "p-b", style: "implements" },
    { id: "3", from: "fake", to: "p-b", style: "implements" },
    { id: "4", from: "http", to: "service", send: "calls" },
    { id: "5", from: "store", to: "x-db", send: "SQL" },
    { id: "6", from: "x-api", to: "http", send: "route" },
    { id: "7", from: "x-caller", to: "service", send: "calls" },
    { id: "8", from: "x-caller", to: "files", send: "reads" },
  ],
};

test("folded, a level hides its adapters with a host and the outside cards only they reach", () => {
  const hidden = hiddenNodes(view, new Set(), new Set());
  assert.deepEqual([...hidden].sort(), ["fake", "http", "store", "x-api", "x-db"]);
});

test("opening a host shows its adapters and what they reach", () => {
  const hidden = hiddenNodes(view, new Set(["p-b"]), new Set());
  assert.deepEqual([...hidden].sort(), ["http", "x-api"]);
});

test("a walk-through's cards show whatever is folded", () => {
  const hidden = hiddenNodes(view, new Set(), new Set(["store", "x-db"]));
  assert.deepEqual([...hidden].sort(), ["fake", "http", "x-api"]);
});

test("hosts sharing an adapter open together, and each lists what folds behind it", () => {
  assert.deepEqual(hostGroup(view, "p-a").sort(), ["p-a", "p-b"]);
  assert.deepEqual(hostGroup(view, "service"), ["service"]);
  const behind = foldedBehind(view);
  assert.deepEqual(behind.get("p-b"), ["store", "fake"]);
  assert.deepEqual(behind.get("service"), ["http"]);
  assert.equal(behind.has("files"), false);
});
