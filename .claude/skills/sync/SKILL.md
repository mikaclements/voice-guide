---
name: sync
description: Check that recent decisions and changes are reflected in every doc that should hold them, using the map below. Run before every push.
---

Bring the docs in line with what was decided. Every fact has one home; other docs link to it instead of restating it. The deliberate exception is a doc for people at the root, which retells its reference twin in `docs/reference/` in plain words: the reference is the home, and the doc for people follows it.

## What each doc holds

The plan, item specs and decision register are working notes, never committed; `CLAUDE.local.md` gives their locations.

| Doc | Answers | Never contains |
|---|---|---|
| `ARCHITECTURE.md` | The design for people: purpose, how it works, the main diagrams, the big decisions and their costs, in plain words, with the same top-level sections as its twin | Labels except in links, every failure mode, rejected alternatives beyond a phrase |
| `docs/reference/architecture.md` | What the system does, how and why, for agents: purpose, scope, qualities (NFR), assumptions (A), use cases (U, W, O, F), decisions (D) boxed where made, failure modes, data model | How code is written, tested or delivered; diagrams, which live in `ARCHITECTURE.md`; use-case examples from particular subjects |
| `CONTRIBUTING.md` | The standards for people, in plain words, with the same top-level sections as its twin | Labels except in links, rules a tool enforces beyond naming the tool, each decision's rejected alternatives |
| `docs/reference/contributing.md` | How any part is built: rules, and the engineering decisions (E) behind them | Feature behaviour |
| `CLAUDE.md` | What an agent needs on every edit: commands, where things are, the extract of the standards, guardrails, gotchas | Feature behaviour, rationale, temporary state |
| `README.md` | How to use voice-guide: what it is, the quickstart, the test embedding, putting an agent on a product, the settings to tune, how it works in brief | Development setup, the commands and the layout (in `CONTRIBUTING.md`); rationale and edge cases, which it links to |
| `schematic/data/schematic.json` | The system drawn level by level for people new to it: each part, what flows between the parts, and the decisions that shape each level, by title | Labels of decisions; the types, which are generated from the code |
| Item spec (working notes) | One item: questions, plan, tests, the pull request split | Anything the design lacks; the design wins on conflict |

## Map: where each kind of change goes

| Kind of change | Home | Also update |
|---|---|---|
| Design decision (behaviour, contract, data, concurrency between parts, security, scale, stack) | A decision box in the section of `docs/reference/architecture.md` that needs it, with the next free D number | `check_docs.py --fix-index`; specs that cite it; `CLAUDE.md` if agents need it on every edit; `ARCHITECTURE.md` if it's a big decision; the schematic's considerations, where a level shows it |
| Engineering decision (code structure, tooling, testing, delivery) | An E entry in `docs/reference/contributing.md` section 14, linked from the rule it explains | The `CLAUDE.md` extract if it changes a rule there |
| Use case, failure mode, quality target, assumption, data model | The feature's section in the design | The item spec; a new use case also in the scope table |
| Coding or testing rule, including one raised in review | `docs/reference/contributing.md` | The `CLAUDE.md` extract if agents need it on every edit; a reply on the review comment |
| Scope (included or excluded for now) | Design section 3 | `ARCHITECTURE.md` section 3 |
| A term added, renamed or redefined | Design Appendix A | Every doc, spec and code name using the old term |
| Order of work, items, status | The plan | |
| Per-item procedure | `.claude/skills/feature/SKILL.md` | |
| A reference doc's change at its people doc's level | The reference | The matching section of its people doc; a section added or renamed on both sides |
| A tracked doc or agent file added, removed or renamed | The file itself | Where things are, in `CONTRIBUTING.md` |
| Agent permissions and hooks | `.claude/settings.json` | The guardrails in `CLAUDE.md` if it's a policy change |
| A route, a route's credential, request, reply or problem type, or the webhook's payload | The code | `api/openapi.yaml` (a test fails on a route it lacks or has extra, not on a changed body); the README where it describes the route |
| A service, package, port or its methods, adapter or connection between parts added, changed or removed | The code | The schematic, through the `schematic` skill (`go test` fails on a package or an import between the parts of voice it doesn't show) |
| A rule that would apply to any project | The playbook | |

## Steps

0. Run `python3 scripts/lib/check_docs.py`; it must pass before and after the sync.
1. List what changed since the last sync: new or edited decisions, and anything agreed in the conversation that isn't written down yet.
2. For each change, look up its row and check the home and every "also update" target. Report a table: change, doc, status (in sync, missing, contradicts).
3. Check the mirrors for drift: each doc for people against its twin, and the `CLAUDE.md` extract against the standards.
4. Check each edited doc stays at its level and in style: plain ASCII; no emojis, bold lead-ins or arrow chains; bullet points for any list of four or more items; scope written as included or excluded for now; no examples from particular subjects in the architecture docs.
5. Fix what's missing or contradictory. Ask before deleting or merging existing content.
6. If a change didn't fit any row, propose a new row for the map.
