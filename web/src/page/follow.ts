// The page follows the conversation: while the user reads along at the bottom of the session, each
// new line, and each reply as it grows, is scrolled into view; once they scroll up to reread, it
// stays put until they scroll back down. On a wide screen the session's column scrolls, on a narrow
// one the whole page does, so what is seen is measured from wherever the session sits.
import { type RefObject, useCallback, useEffect, useLayoutEffect, useRef } from "react";

// How far below what the user sees the session's bottom may sit and still count as reading along:
// two transcript lines (16px text at a line height of 1.5), so a scroll that stops just short of the
// bottom still follows. A round guess, to be tuned by use.
export const followSlackPx = 48;

// Whether the page should keep following, from where the session's bottom sits and the part of the
// window the user sees, top and bottom, in the same coordinates: yes while the bottom is in view or
// just below it; no once it is further below, or above, as when the user scrolled down past it to
// the past sessions beneath it on a narrow screen.
export function follows(bottom: number, seen: { top: number; bottom: number }): boolean {
  return bottom >= seen.top && bottom <= seen.bottom + followSlackPx;
}

// Whether the user reads along: where the marker at the session's bottom sits against the part of
// the window it can be seen in, which is the window cut down by each ancestor that scrolls.
function readingAlong(marker: Element): boolean {
  const seen = { top: 0, bottom: window.innerHeight };
  for (let p = marker.parentElement; p; p = p.parentElement) {
    if (getComputedStyle(p).overflowY === "visible") continue;
    const box = p.getBoundingClientRect();
    seen.top = Math.max(seen.top, box.top);
    seen.bottom = Math.min(seen.bottom, box.bottom);
  }
  return follows(marker.getBoundingClientRect().top, seen);
}

// The ref for an empty marker after the session's last line, which is kept in view each time drawn,
// what the session shows, changes, while the user reads along; nothing is followed before a session
// shows, so filling in the start form never moves the page. Whether the user reads along is
// measured on each scroll and once each change is in, so it stands as it was before the next change
// pushes the bottom down.
export function useFollow(drawn: unknown): RefObject<HTMLDivElement | null> {
  const marker = useRef<HTMLDivElement>(null);
  const following = useRef(true);
  // Kept as it was while the session isn't drawn, as with the Agents tab shown, so coming back finds
  // the user where they left off.
  const measure = useCallback(() => {
    const el = marker.current;
    if (el?.getClientRects().length) following.current = readingAlong(el);
  }, []);
  useEffect(() => {
    // A scroll doesn't bubble, so it is caught on its way down, from whichever part scrolled.
    const options = { capture: true, passive: true };
    document.addEventListener("scroll", measure, options);
    window.addEventListener("resize", measure);
    return () => {
      document.removeEventListener("scroll", measure, options);
      window.removeEventListener("resize", measure);
    };
  }, [measure]);
  // Before the browser paints, so a new line never shows below the fold first. Nearest moves only as
  // far as needed, so a bottom already in view stays where it is. Measured after, for a change that
  // moves the bottom with no scroll, as a new session's short transcript replacing a long one.
  useLayoutEffect(() => {
    if (drawn && following.current) marker.current?.scrollIntoView({ block: "nearest" });
    measure();
  }, [drawn, measure]);
  return marker;
}
