---
name: doc-audit
description: Full read-through of every project doc to find and fix inconsistencies, broken references, misplaced content and style problems. Use after a round of design changes; it reads every doc, so not routinely.
---

Read every file below end to end, then fix what's wrong. Ask before deleting or merging content; fix everything else directly.

Files: `README.md`, `ARCHITECTURE.md`, `CLAUDE.md`, `docs/**/*.md`, the working notes (every `.md` file `CLAUDE.local.md` routes to, unless it has a `docs-check: skip` line), `.claude/skills/*/SKILL.md`, `.claude/agents/*.md`, `.claude/settings.json`.

## Mechanical checks (run first)

Run `python3 scripts/lib/check_docs.py` and fix everything it reports. After adding a decision or changing a decision's title, run it with `--fix-index` to rebuild the decision index in reading order; a new decision takes the next free number, and no label is ever renumbered. It checks every relative link and anchor in the tracked docs, that labels are defined once and numbered without gaps, that every label mentioned in the design is a link, decision boxes and their fields, the decision index, that every use case is listed in the scope and every quality has a target, that labels cited in other docs and the working notes exist, that the pair's top-level sections match, characters that break Mermaid diagrams, and plain ASCII. It can't tell whether a diagram renders well: after changing one, render it and look.

What it can't see:
1. A cited label that exists but means something else than the citing sentence says.
2. Every endpoint has a use case with the same success and failure behaviour, once endpoints exist.
3. Every item ID and spec file name matches the plan.
4. Every file path mentioned exists, or is explicitly planned in an item spec.
5. Every table and column in a feature's data model matches the migrations, once they exist.

## Reading checks

6. Contradictions: the same fact stated differently in two places (numbers, statuses, rules, names).
7. Level: the design holds what the system does, how and why; never tests, tooling, build status or delivery items. Specs cite decisions instead of restating them.
8. Status: a decision's status appears only in its box; nothing treats a proposed decision as settled.
9. Duplication: a fact restated in a second doc instead of linked. Known, intended mirrors: `ARCHITECTURE.md`, which retells its reference twin in plain words, and the conventions extract in `CLAUDE.md`.
10. Terminology: every doc uses the terms in the design's Appendix A with their single meaning, and the two scope groups (included, excluded for now); ideas nobody plans to build are not written down.
11. Shipped files (README, CLAUDE.md, ARCHITECTURE.md, docs/, .claude/) describe the system only: no dates of drafting, notes or conversation residue. Working notes stay out of shipped files.
12. Style: no emojis, bold lead-ins, arrow chains, personal references or unused checkboxes; plain prose.

## Report

A table of findings: file, problem, fix applied (or "needs a decision"). End with anything that needs the developer's call, one question at a time.
