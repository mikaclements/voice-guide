<!-- Title: type(scope): summary, like a commit (imperative, lower-case, no period), same type as the branch. -->
<!-- Four sections, about 300 words in all. Detail beyond them stays in the commits. -->

## Summary
<!-- Two lines first, each with a small example from someone using it:
**Before:** what happens today. **After:** what happens with this pull request.
**Before:** a caller who hangs up mid-reply leaves the turn in flight, and the session refuses the next turn until the turn deadline passes.
**After:** the turn is saved `lost` with "Let's start with Harbourline," as its text, and the next turn is taken at once.
Then one bullet per change, in plain words: what happens now, then **Why:** and a short phrase, usually the problem it fixes.
- **Caller hangs up mid-reply:** the turn is saved `lost` with the text so far. **Why:** the turn was never saved and the session stayed blocked. -->

## Diagram
<!-- A small picture of what changes, in a code block, plain ASCII only (-->, x, <=), every box edge lined up: a before and after, or the boxes and tables the change touches, with `*` on what's new and a few example rows. Leave out for docs-only changes. -->

## Key files
<!-- Two to four files in reading order, one line each saying what to look at. Name generated code with its source and the command that makes it. -->

## Notes
<!-- One line each:
- Your calls: each choice a reviewer could question, with **Why:**.
- Numbers: each deadline, limit or default this sets, with **Why:** measured, derived from what, or a round guess and when it will be measured.
- Changed tests: any existing test changed and why, or "none".
- Checked: the commands run and their results, each gate seen failing, what was done by hand; "Not run:" what wasn't.
- Size: lines added and removed, generated files and lockfiles shown apart. -->
