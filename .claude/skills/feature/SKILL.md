---
name: feature
description: Run the build loop for one item of the plan (I1, I2 and so on): spec, plan, tests first, implement, verify, review, sync, pull request.
argument-hint: "[item-id, e.g. i1]"
disable-model-invocation: true
---

Run item $0 of the plan. The plan, the item specs and their template are working notes; `CLAUDE.local.md` gives their locations.

1. Spec. Open the item's spec (`<id>-<name>.md`; create it from the template if missing). Confirm the use cases it delivers against their feature's section in `docs/reference/architecture.md` (caller, flow, failure modes); a use case or failure mode the design is missing is worked out with the developer and added there first. Then work through its open questions with the developer, one at a time. A real decision (one where a reviewer could ask "why not X?") gets a decision box in the design or an engineering decision in `docs/reference/contributing.md`, each with what enforces it. No code until the spec's status is `Agreed`.
2. Read. The item's row in the plan; its features' whole sections in the design; the platform section; the terminology (design Appendix A); the scope (design section 4); and the standards, especially sections 4 (sync, async and streaming) and 5 (modules and hexagonal layout).
3. Pre-mortem, before any code. Walk each flow the item adds or changes through these scenarios and write into the spec, for each that applies, what must happen and the test that proves it:
   - two or more copies of the service at once;
   - a crash or restart mid-step;
   - a dependency slow, down or at its cap, and for a long time;
   - the same request twice, and retries;
   - races with every other path touching the same rows, connection or turn;
   - settings and limits at their extremes;
   - the user acting mid-flow: ending, talking, reconnecting, retrying;
   - order in time: what was saved first against what happened first.

   Write down the scenarios that hit the obvious path, the failures users meet in ordinary use, and security; an extreme edge case (a rare race, a window of a second, a subtlety of a rolling deploy) goes on the hardening list in the working notes instead, for the hardening phase's thorough search. A scenario the design doesn't answer is a failure mode worked out with the developer and added to the design first, never left to be found in review. The list goes into every builder's brief and to the reviewer.
4. Plan. Write into the spec: the files to create or change (the core in the module's folder, each adapter in its own subfolder, a fake for every new port); the tests, each mapped to a use case, a failure mode, a quality target or a decision; and how the item splits into pull requests (one idea each, planned at 300 to 600 lines and never past 800, including tests and docs, each with its estimate, none that only prepares for another; by default decisions and domain, then the happy path, then one per failure mode). Wait for the developer's go-ahead.
5. Branch each pull request from an up-to-date `main`, or from the one below it when stacked. Tests first for the core: write the failing tests from the design, against fake adapters, then the code.
6. Implement end to end: migration, store adapter, core, inbound adapter, tests. The design's data model must match the migrations.
7. Schematic, then verify. Run the `schematic` skill for every service, package, port or method, adapter, connection or decision the item added, changed or removed, so its checks run with the rest; then run the `verify` skill and show its evidence table.
8. Review. Have the `design-reviewer` subagent review the files this item changed, giving it the pre-mortem list: it checks the code against the list and looks for what the list missed; a finding the list should have held is added to it. Fix high or medium findings on the obvious path or security; low findings and edge cases go on the hardening list; list anything you disagree with and why. One review round; a second only when a fix touched security. Once it lands, run the feature end to end.
9. Sync. Run the `sync` skill: new or changed decisions (then `check_docs.py --fix-index`), the design if the build showed something new, CONTRIBUTING.md's commands for new commands, the README for what a user or a product does differently, such as a new endpoint, the plan's status.
10. Report the files changed, the evidence, the review findings and the spots the developer should read most carefully. Then commit, push and open the pull request, as directed work (the guardrail in `CLAUDE.md`), with a short description from the template. Never merge it.
11. Pull request review. Read the developer's comments with `gh` (conversation comments, inline comments, review summaries). Fix each; when a comment raises a rule, add it to the standards in the same pull request and reply saying what changed. Never resolve a thread; the developer does after checking.
12. After the merge: switch to `main`, pull, and update the plan (the item's status and "Where we are").
