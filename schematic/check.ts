// The schematic's visual check, run by `scripts/schematic.sh check` after a build: it serves the
// built page on 5181 while it runs, beside any page already open on 5180, and opens in headless
// Chrome at 1440x900 every level as it opens, then with every adapter and label shown, with a
// host card clicked (its adapters shown, its panel open), and at the middle step of each
// walk-through. It saves a
// screenshot of each into .shots/ and reports cards, labels or captions that overlap and text cut
// off, as the browser measures them. It exits non-zero when it finds any.
import { type ChildProcess, spawn } from "node:child_process";
import { mkdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { chromium, type Page } from "playwright-core";
import type { Schematic } from "./src/types.ts";

const here = import.meta.dirname;
const out = join(here, ".shots");
const port = "5181";
const url = `http://127.0.0.1:${port}`;
const schematic: Schematic = JSON.parse(readFileSync(join(here, "data", "schematic.json"), "utf8"));

function serve(): Promise<ChildProcess> {
  const server = spawn("npx", ["vite", "preview", "--port", port], {
    cwd: here,
    stdio: ["ignore", "pipe", "pipe"],
  });
  return new Promise((resolve, reject) => {
    // A server that never said it was ready is stopped, so it can't hold the port for the next run.
    const timer = setTimeout(() => {
      server.kill();
      reject(new Error("vite preview didn't start within 15 s"));
    }, 15_000);
    server.stdout?.on("data", (b: Buffer) => {
      if (String(b).includes(port)) {
        clearTimeout(timer);
        resolve(server);
      }
    });
    server.on("exit", (code) =>
      reject(new Error(`vite preview exited with ${code}; is ${port} taken?`)),
    );
  });
}

async function open(page: Page, id: string, theme = "light", query = "") {
  await page.goto(`${url}/?theme=${theme}${query}#${id}`);
  await page.waitForSelector(".react-flow__node", { timeout: 15_000 });
  // The level settles in about half a second.
  await page.waitForTimeout(1100);
}

// problems lists cards and labels that overlap each other, and text cut off in a card, a label
// or a group's header.
function problems(page: Page): Promise<string[]> {
  return page.evaluate(() => {
    const found: string[] = [];
    const boxes = [...document.querySelectorAll(".card, .cls, .pill, .flowbar, .start-here")].map(
      (el) => ({
        r: el.getBoundingClientRect(),
        name: (
          el.querySelector(".card-name, .cls-name")?.textContent ??
          el.textContent ??
          ""
        ).slice(0, 40),
      }),
    );
    for (let i = 0; i < boxes.length; i++) {
      for (let j = i + 1; j < boxes.length; j++) {
        const a = boxes[i].r;
        const b = boxes[j].r;
        const ox = Math.min(a.right, b.right) - Math.max(a.left, b.left);
        const oy = Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top);
        if (ox > 1 && oy > 1) found.push(`overlap: ${boxes[i].name} / ${boxes[j].name}`);
      }
    }
    for (const el of document.querySelectorAll(".card, .cls")) {
      const name = el.querySelector(".card-name, .cls-name")?.textContent;
      if (el.scrollHeight > el.clientHeight + 1) found.push(`cut off at the bottom: ${name}`);
      for (const line of el.querySelectorAll(
        ".card-summary > div, .card-members > div, .card-setting > div, .card-path, .card-tech, .card-name, .cls-line, .cls-name",
      )) {
        if (line.scrollWidth > line.clientWidth + 1)
          found.push(`cut off at the side: ${line.textContent?.slice(0, 50)}`);
      }
    }
    for (const el of document.querySelectorAll(".flowbar-caption, .card-footer span")) {
      if (el.scrollHeight > el.clientHeight + 1 || el.scrollWidth > el.clientWidth + 1)
        found.push(`cut off: ${el.textContent?.slice(0, 50)}`);
    }
    for (const head of document.querySelectorAll(".box-head")) {
      const right = head.getBoundingClientRect().right;
      for (const part of head.querySelectorAll(".box-eyebrow, .box-title")) {
        if (part.getBoundingClientRect().right > right + 0.5)
          found.push(`group header cut off: ${head.textContent?.slice(0, 50)}`);
      }
    }
    for (const el of document.querySelectorAll(".pill")) {
      // A label drawn under a card or a group's box is there but can't be seen.
      const r = el.getBoundingClientRect();
      const top = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      const onScreen = r.right > 0 && r.bottom > 0 && r.left < innerWidth && r.top < innerHeight;
      if (onScreen && top && !el.contains(top))
        found.push(`label hidden under something: ${el.textContent?.slice(0, 50)}`);
      const inner = el.querySelector(".pill-grid, .pill-via");
      if (
        inner &&
        (inner.scrollWidth > el.clientWidth ||
          inner.getBoundingClientRect().bottom > el.getBoundingClientRect().bottom + 1)
      ) {
        found.push(`label cut off: ${el.textContent?.slice(0, 50)}`);
      }
    }
    return [...new Set(found)];
  });
}

mkdirSync(out, { recursive: true });
const server = await serve();
let failed = false;
try {
  const browser = await chromium.launch({ channel: "chrome", headless: true });
  const page = await browser.newPage({
    viewport: { width: 1440, height: 900 },
    deviceScaleFactor: 2,
  });
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  const shoot = async (name: string) => {
    await page.screenshot({ path: join(out, `${name}.png`) });
    const found = await problems(page);
    failed ||= found.length > 0;
    console.log(`${name}: ${found.length ? `\n  ${found.join("\n  ")}` : "ok"}`);
  };
  for (const view of schematic.views) {
    await open(page, view.id);
    await shoot(view.id);
    if (view.type === "classes") continue;
    // A click on a card with adapters folded behind it shows them and opens its panel.
    const host = page.locator(".card:has(.card-fold) .card-name").first();
    if (await host.count()) {
      await host.click();
      await page.waitForTimeout(900);
      await shoot(`${view.id}-opened`);
    }
    await open(page, view.id, "light", "&adapters=all&labels=all");
    await shoot(`${view.id}-all`);
    for (const f of view.flows ?? []) {
      const step = Math.ceil(f.steps.length / 2);
      await open(page, view.id, "light", `&flow=${f.id}&step=${step}`);
      await shoot(`${view.id}-walk-${f.id}`);
    }
  }
  await open(page, schematic.views[1]?.id ?? schematic.views[0].id, "dark");
  await page.screenshot({ path: join(out, "dark.png") });
  const first = schematic.views[0];
  if (first.flows?.length) {
    await open(page, first.id, "dark", `&flow=${first.flows[0].id}&step=2`);
    await page.screenshot({ path: join(out, "dark-walk.png") });
  }
  await browser.close();
  if (errors.length) {
    failed = true;
    console.log(`page errors:\n  ${errors.join("\n  ")}`);
  }
  console.log(`screenshots: ${out}`);
} finally {
  server.kill();
}
process.exit(failed ? 1 : 0);
