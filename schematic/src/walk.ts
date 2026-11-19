// Stepping through a level's walk-throughs: next and back within a story, and on into the next
// story when played in order.
import type { View } from "./types.ts";

export interface Walk {
  flow: string;
  step: number;
}

// walkAfter is the step after w: the next in its story, else the first of the next story, else
// none.
export function walkAfter(view: View, w: Walk): Walk | null {
  const flows = view.flows ?? [];
  const i = flows.findIndex((f) => f.id === w.flow);
  if (i < 0) return null;
  if (w.step < flows[i].steps.length - 1) return { flow: w.flow, step: w.step + 1 };
  const next = flows[i + 1];
  return next ? { flow: next.id, step: 0 } : null;
}

// walkBefore is the step before w: the previous in its story, else the last of the story before,
// else none.
export function walkBefore(view: View, w: Walk): Walk | null {
  const flows = view.flows ?? [];
  const i = flows.findIndex((f) => f.id === w.flow);
  if (i < 0) return null;
  if (w.step > 0) return { flow: w.flow, step: w.step - 1 };
  const prev = flows[i - 1];
  return prev ? { flow: prev.id, step: prev.steps.length - 1 } : null;
}

// How long a played step stays: long enough to read its caption at an easy pace, about 45 ms a
// character (a round guess, near 250 words a minute, tried on the captions by eye), and never
// under 3.5 s, so the lit cards can be found first.
export function stepMillis(caption: string): number {
  return Math.max(3500, caption.length * 45);
}
