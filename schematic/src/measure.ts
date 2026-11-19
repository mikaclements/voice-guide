// Text measurement for sizing nodes and edge labels before layout, with the same fonts the CSS uses,
// so ELK reserves exactly the room each card and label takes and nothing overlaps.

export const SANS =
  '-apple-system, BlinkMacSystemFont, "SF Pro Text", "Segoe UI", Inter, Roboto, "Helvetica Neue", Arial, sans-serif';
export const MONO =
  'ui-monospace, "SF Mono", SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace';

let ctx: CanvasRenderingContext2D | null = null;
const cache = new Map<string, number>();

export function textWidth(text: string, size: number, weight = 400, mono = false): number {
  const key = `${size}|${weight}|${mono ? 1 : 0}|${text}`;
  const hit = cache.get(key);
  if (hit !== undefined) return hit;
  // Outside a browser, as in the tests, an estimate: about the average width of these fonts.
  if (!ctx && typeof document !== "undefined")
    ctx = document.createElement("canvas").getContext("2d");
  if (!ctx) return text.length * size * (mono ? 0.6 : 0.55);
  ctx.font = `${weight} ${size}px ${mono ? MONO : SANS}`;
  const w = ctx.measureText(text).width;
  cache.set(key, w);
  return w;
}

// wrap breaks text into lines no wider than max; a word longer than max gets a line of its own.
export function wrap(
  text: string,
  max: number,
  size: number,
  weight = 400,
  mono = false,
): string[] {
  const words = text.split(/\s+/).filter(Boolean);
  const lines: string[] = [];
  let line = "";
  for (const word of words) {
    const next = line ? `${line} ${word}` : word;
    if (!line || textWidth(next, size, weight, mono) <= max) {
      line = next;
    } else {
      lines.push(line);
      line = word;
    }
  }
  if (line) lines.push(line);
  return lines;
}

// clampLines wraps and keeps at most n lines, ending the last with an ellipsis when cut.
export function clampLines(
  text: string,
  max: number,
  n: number,
  size: number,
  weight = 400,
): string[] {
  const lines = wrap(text, max, size, weight);
  if (lines.length <= n) return lines;
  const kept = lines.slice(0, n);
  let last = `${kept[n - 1]}…`;
  while (textWidth(last, size, weight) > max && last.length > 2) {
    last = `${last.slice(0, -2).trimEnd()}…`;
  }
  kept[n - 1] = last;
  return kept;
}

// truncate shortens one line to fit max, with an ellipsis.
export function truncate(
  text: string,
  max: number,
  size: number,
  weight = 400,
  mono = false,
): string {
  if (textWidth(text, size, weight, mono) <= max) return text;
  let t = text;
  while (t.length > 1 && textWidth(`${t}…`, size, weight, mono) > max) t = t.slice(0, -1);
  return `${t.trimEnd()}…`;
}
