// The transcript follows the conversation: while the user reads along at its bottom, each new line,
// and each reply as it grows, lands in view; once they scroll up to reread, it stays put until they
// scroll back down or jump to the latest line. The transcript scrolls in a box of its own on every
// screen, so only that box's own scrolling counts, never the page's.
import { type RefObject, useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";

// How far above its bottom the transcript may be scrolled and still count as reading along: a few
// pixels, for a layout in fractions of a pixel, since the box stops at its own bottom; any more and a
// small scroll up while a reply streams in would be pulled back down. As the drop-in element does.
export const followSlackPx = 4;

// Whether the transcript should keep following, from how far its box is scrolled: yes while what
// is shown reaches its bottom, or stops short of it by no more than the slack.
export function follows(box: { scrollTop: number; clientHeight: number; scrollHeight: number }) {
  return box.scrollHeight - box.scrollTop - box.clientHeight <= followSlackPx;
}

// The ref for the transcript's box, kept scrolled to its bottom each time drawn, what the session
// shows, changes, while the user reads along; whether they paused it by scrolling up; and a jump
// back to the latest line. Whether they read along is measured on each scroll of the box and once
// each change is in, so it stands as it was before the next change grows the transcript.
export function useFollow(drawn: unknown): {
  box: RefObject<HTMLElement | null>;
  paused: boolean;
  jump(): void;
} {
  const box = useRef<HTMLElement>(null);
  const following = useRef(true);
  const [paused, setPaused] = useState(false);
  // Kept as it was while the box isn't drawn, as with the Agents tab shown, so coming back finds
  // the user where they left off.
  const measure = useCallback(() => {
    const el = box.current;
    if (!el?.getClientRects().length) return;
    following.current = follows(el);
    setPaused(!following.current);
  }, []);
  // To the bottom while the user reads along, and measured either way.
  const keep = useCallback(() => {
    const el = box.current;
    if (el && following.current) el.scrollTop = el.scrollHeight;
    measure();
  }, [measure]);
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    // Drawn again, as when the Test tab shows after the transcript grew out of sight, or resized.
    const resized = new ResizeObserver(keep);
    resized.observe(el);
    el.addEventListener("scroll", measure, { passive: true });
    return () => {
      resized.disconnect();
      el.removeEventListener("scroll", measure);
    };
  }, [keep, measure]);
  // Before the browser paints, so a new line never shows out of view first. Only the box scrolls,
  // so the page stays where the user has it.
  useLayoutEffect(() => {
    if (drawn) keep();
  }, [drawn, keep]);
  const jump = useCallback(() => {
    following.current = true;
    keep();
  }, [keep]);
  return { box, paused, jump };
}
