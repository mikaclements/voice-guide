---
name: drive-session
description: Drive a session from the terminal as a product's server and the user's browser would (start it, take typed turns, end it). Use to try a change to sessions, turns or the agent against the running service without the web page.
---

Needs `scripts/run.sh` running in another terminal (`MODEL_PROVIDER=fake scripts/run.sh` costs nothing; otherwise each turn calls the real model: the Claude API by default, billed to the key in `.env`, or the developer's Claude login with `MODEL_PROVIDER=claude-subscription`). Run the scripts from the repository root; they read `.env`, which the first run creates.

1. `.claude/skills/drive-session/start-session.sh [REQUEST_ID]` plays the product's server: it starts a session with the mock interviewer, with the development product key, for the placeholder user's reference, sending their resume from `seed/placeholder-user/` as session context. It prints the session and keeps its ticket under `.tickets/` (git ignores it), where the next two scripts find it by the session's ID. Passing the printed `request_id` again returns the same session with a new ticket.
2. `.claude/skills/drive-session/turn.sh SESSION_ID "words"` plays the user's browser: a typed turn with the session's ticket. It prints the reply as it streams, then the final event (`done` with the turn's ID and number, or `lost`), and the time to first text on standard error; a refused turn prints its problem.
3. `.claude/skills/drive-session/end-session.sh SESSION_ID` ends the session with its ticket, so it takes no more turns, and prints it with when it ended; a background job then saves its summary for the agent's next session. A session with no turn and no speech for 5 minutes ends by itself.

`VOICE_ADDR` points the scripts at a voice other than `.env`'s.
