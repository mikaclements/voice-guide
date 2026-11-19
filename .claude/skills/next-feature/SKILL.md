---
name: next-feature
description: Start a session from the last handoff, propose the next box (goal, chunks, out of the box, done-when), build inside it once the developer confirms, and close it with a new handoff. Run it right after /clear.
disable-model-invocation: true
---

Run this right after `/clear`: a skill can't clear the conversation itself. The plan, its handoff, the item specs and the decision register are working notes; `CLAUDE.local.md` gives their locations.

1. Where we are. Read the latest handoff at the end of the plan, then check it against what is real now:
   - `git fetch`, and main's last CI run;
   - open pull requests (`gh pr list`);
   - worktrees left over (`git worktree list`).

   Report in a few lines what's on main, what's open, and what changed since the handoff.
2. Propose the box, built from the handoff's "next" and the plan:
   - a goal: one sentence on what the developer can do at the end that they can't now;
   - 3 to 5 chunks, in order, each one pull request or one decision, each with its design sketched;
   - what stays out of the box;
   - done when: the command or check that proves the goal.

   A chunk that needs a design decision first gets research (what's available, what others do, with sources) and the options weighed in a table, then one recommendation with its Why. Wait for the developer to confirm or cut the box. No code before that.
3. Work inside the box. Each pull request follows the `feature` skill's loop: tests first, the schematic, verify, review, sync, and a description in the template's four sections.
   - Anything new that comes up becomes its own small box, or is queued. It is never tacked onto this one.
   - After the developer reports a merge, check main's CI and the open pull requests.
   - If the conversation grows long at a natural boundary, write the handoff (step 4) and suggest clearing.
4. Close the box when its done-when is met, without being asked:
   1. Say so, with the evidence.
   2. Update the plan's status and the decision register.
   3. Write a dated handoff at the end of the plan: main's state; the next box to propose; the decisions it relies on; working rules set this session; open questions for the developer; anything set aside.
   4. Tell the developer to run `/clear`, then `/next-feature`.
