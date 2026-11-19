// Drives the test page in headless Chrome as a person typing would: starts a session with the mock
// interviewer, waits for its greeting to finish sounding, then sends each scripted answer once the
// reply before it has finished, and ends the session. Prints one JSON line per reply, on the wall
// clock voice's stage times use: when the user pressed (Start or Send), when the reply's first
// audio packet reached the browser, when its first sound came out of the browser's audio, the page's
// own line beneath the reply, and the quiet stretches inside the reply. Then one line with the
// browser's jitter buffer: its average delay and its target over the whole session.
//   node check-page.mts URL [TURNS]
import { chromium } from "playwright-core";

const [url, turnsArg = "5"] = process.argv.slice(2);
const turns = Number(turnsArg);
const said = [
  "I led the rebuild of shipment tracking at Harbourline: status updates moved from nightly batch files to an event stream.",
  "The hardest part was keeping the read model consistent when events arrived out of order.",
  "We gave every event a version per shipment and dropped any older than the one already applied.",
  "Support tickets about wrong statuses fell by about two thirds in the first quarter.",
  "I'd measure it again before changing anything else.",
];
// A reply has finished sounding after this much quiet: longer than any pause inside one, which the
// page run measured at under a second.
const doneQuietMs = 1500;
// A quiet stretch inside a reply at least this long is a pause between phrases: a round guess
// between the gaps between words, shorter, and the pauses at commas, 0.25 s and longer.
const pauseMs = 150;
// The longest wait for one reply to finish sounding before the run fails: a round guess, several
// times the longest reply seen (about 15 s), so a greeting that never sounds ends the run.
const replyDeadlineMs = 60_000;

type Probe = {
  presses: number[];
  loud: [number, boolean][];
  packets: number[];
  jitter?: { delay: number; target: number; emitted: number };
};

// Runs in the page before its own scripts. Keeps each audio connection the page opens and, once
// the agent's track arrives, reads its level every 5 ms, twice as often as the page, on the
// page's line (sound is above 0.01, -40 dB), recording each change, and the connection's receive
// statistics every 10 ms, recording when the first packet after 100 ms without one arrived: the
// last packet's arrival, which the statistics give, less 20 ms for each packet after the first,
// since Chrome hands out statistics a few tens of ms old. Records each press of Start or Send.
function probe() {
  const p: Probe = { presses: [], loud: [], packets: [] };
  (window as unknown as { probe: Probe }).probe = p;
  document.addEventListener(
    "click",
    (e) => {
      const b = (e.target as HTMLElement).closest("button");
      if (b && /^(Start|Send)$/.test(b.textContent ?? "")) p.presses.push(Date.now());
    },
    true,
  );
  const Original = window.RTCPeerConnection;
  const Wrapped = function (this: unknown, ...args: ConstructorParameters<typeof RTCPeerConnection>) {
    const pc = new Original(...args);
    pc.addEventListener("track", (e) => {
      const context = new AudioContext();
      const analyser = context.createAnalyser();
      analyser.fftSize = 512;
      context.createMediaStreamSource(new MediaStream([e.track])).connect(analyser);
      const samples = new Float32Array(analyser.fftSize);
      let was = false;
      setInterval(() => {
        analyser.getFloatTimeDomainData(samples);
        const loud = samples.some((s) => Math.abs(s) > 0.01);
        if (loud !== was) p.loud.push([Date.now(), loud]);
        was = loud;
      }, 5);
      let received = 0;
      let lastAt = 0;
      setInterval(async () => {
        const stats = await pc.getStats(e.track);
        stats.forEach((s) => {
          if (s.type !== "inbound-rtp" || s.kind !== "audio") return;
          if (s.packetsReceived > received) {
            const first = s.lastPacketReceivedTimestamp - (s.packetsReceived - received - 1) * 20;
            if (first - lastAt > 100) p.packets.push(first);
            lastAt = s.lastPacketReceivedTimestamp;
            received = s.packetsReceived;
          }
          p.jitter = {
            delay: s.jitterBufferDelay,
            target: s.jitterBufferTargetDelay,
            emitted: s.jitterBufferEmittedCount,
          };
        });
      }, 10);
    });
    return pc;
  } as unknown as typeof RTCPeerConnection;
  Wrapped.prototype = Original.prototype;
  window.RTCPeerConnection = Wrapped;
}

const browser = await chromium.launch({
  channel: "chrome",
  headless: true,
  args: [
    "--use-fake-ui-for-media-stream",
    "--use-fake-device-for-media-stream",
    "--autoplay-policy=no-user-gesture-required",
  ],
});
try {
  const page = await browser.newPage();
  await page.addInitScript(probe);
  await page.goto(url);
  await page.locator("select").selectOption({ label: "Mock interviewer" });
  await page.getByRole("button", { name: "Start", exact: true }).click();

  const read = () => page.evaluate(() => (window as unknown as { probe: Probe }).probe);
  // Waits until the reply to the latest press has sounded and been quiet for doneQuietMs, and the
  // page has written its line beneath it; fails on the page showing an error, a reply lost or its
  // audio stopped, or the reply taking longer than replyDeadlineMs.
  const finished = async (replies: number) => {
    const deadline = Date.now() + replyDeadlineMs;
    for (;;) {
      if (Date.now() > deadline) throw new Error(`reply ${replies - 1} didn't finish sounding in time`);
      if (await page.locator(".error").count())
        throw new Error(`the page shows: ${await page.locator(".error").innerText()}`);
      const shown = (await page.locator(".turn .meta").allTextContents()).join(" ");
      if (/the reply was lost|the audio stopped/.test(shown)) throw new Error(`the page shows: ${shown}`);
      const p = await read();
      const press = p.presses.at(-1) ?? 0;
      const last = p.loud.at(-1);
      const sounded = p.loud.some(([t, loud]) => loud && t > press);
      const metas = await page.locator(".turn .meta").count();
      if (sounded && last && !last[1] && Date.now() - last[0] > doneQuietMs && metas >= replies) return;
      await page.waitForTimeout(100);
    }
  };

  await finished(1);
  for (let i = 0; i < turns; i++) {
    await page.locator("form input").fill(said[i % said.length]);
    await page.getByRole("button", { name: "Send", exact: true }).click();
    await finished(i + 2);
  }
  const metas = await page.locator(".turn .meta").allTextContents();
  await page.getByRole("button", { name: "End", exact: true }).click();
  await page.getByText("(ended)").waitFor();

  const p = await read();
  p.presses.forEach((press, i) => {
    const next = p.presses[i + 1] ?? Number.POSITIVE_INFINITY;
    const changes = p.loud.filter(([t]) => t > press && t < next);
    const firstSound = changes.find(([, loud]) => loud)?.[0];
    const firstPacket = p.packets.find((t) => t > press && t < next);
    const pauses: number[] = [];
    for (let j = 1; j < changes.length - 1; j++) {
      const [quietAt, loud] = changes[j];
      if (!loud && changes[j + 1][0] - quietAt >= pauseMs) pauses.push(changes[j + 1][0] - quietAt);
    }
    console.log(
      JSON.stringify({
        reply: i,
        press,
        first_packet: firstPacket && Math.round(firstPacket),
        first_sound: firstSound,
        page: metas[i],
        pauses_ms: pauses,
      }),
    );
  });
  const j = p.jitter;
  if (j?.emitted) {
    console.log(
      JSON.stringify({
        jitter_buffer_ms: Math.round((1000 * j.delay) / j.emitted),
        jitter_target_ms: Math.round((1000 * j.target) / j.emitted),
      }),
    );
  }
} finally {
  await browser.close();
}
