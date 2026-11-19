---
name: measure
description: Measure voice-guide against the real model and services (model, text-to-speech, speech-to-text, spoken turns, summaries). Use when a change is meant to make something faster, to reproduce a speed number in the docs, or to check a real vendor by hand.
---

The scripts in this folder measure what unit tests can't: the real model, Kokoro and Moonshine. Run them from the repository root, after `npm ci` once. Every script but `check-model.sh` needs Docker running; `check-model.sh` needs `jq`, and `check-stt.sh` needs `uv`. Report each run's output, with the machine and the model, beside the number it backs.

A script on the real model calls it on the developer's Claude login and draws from its usage limits, or with `MODEL_PROVIDER=claude-api` (for `check-model.sh`, `PROVIDER` `claude-api`) bills the API key in `.env`: run it only when asked, or when the measurement is the point of the change. `MODEL_PROVIDER=fake` (for `check-model.sh`, `PROVIDER` `fake`) costs nothing and still exercises everything else. The scripts that run their own voice publish a copy of `agents/` without each file's `model:` line, so the model a run names is the one that answers, and print the model the turns recorded.

The scripts on their own database and ports leave a running `scripts/run.sh` alone; each refuses a port already taken and says which to pick instead. Their database is dropped when they exit.

| Script | What it proves | Arguments | What it starts | Real model |
|---|---|---|---|---|
| `check-model.sh` | Scripted turns in one session through the model service: each streamed reply, its time to first text and usage | `[MODEL] [PROVIDER] [TURNS]`, defaults `claude-sonnet-5-5`, `claude-subscription`, 3 | Its own model service on `MODEL_PORT` (8091 unless set); no database | Yes, unless `PROVIDER` is `fake` |
| `compare-models.sh` | The same typed turns with each model: each stage's median and 90th percentile, and the share of the prompt read from cache | `[TURNS] [MODEL...]`, defaults 5, Sonnet and Haiku | Its own database, model service and voice (ports 18091 and 18080) | Yes, unless `MODEL_PROVIDER=fake` |
| `check-tts.sh` | One reply spoken through Kokoro as the model streams it, played in real time: time to first audio, any wait for a later phrase | none | Kokoro in compose (the first start pulls a 4.5 GB image) | No |
| `check-stt.sh` | A sentence Kokoro speaks, streamed into speech-to-text at its pace: the text, the time from the audio's end to the final text, and the end-of-turn check on a finished and an unfinished sentence | none; `STT_MODEL` picks Moonshine's size | The speech-to-text service (built) and Kokoro in compose | No |
| `check-speak.sh` | Spoken turns end to end, a Go program playing the browser: what voice heard, each reply, the time from the sentence's last packet to the reply's first audio, voice's stage times, then how soon a reply stops when cut in on; then greetings: sessions whose agent opens, each with its own context line, each greeting's first audio from the session's start and from the audio connection coming up, and voice's stage times; session 1, the coldest, apart, then median and 90th percentile | `[TURNS] [MODEL...]`, defaults 3, Sonnet and Haiku; `GREETINGS` sessions, 10 unless set, 0 for none | Kokoro and speech-to-text in compose, plus its own database, model service and voice (ports 18091 and 18080) | Yes, unless `MODEL_PROVIDER=fake` |
| `check-summary.sh` | A session's summary is saved after it ends, and how long that took, and the next session opens with it | `[MODEL]`, default `claude-haiku-5-5` | Its own database, model service and voice (ports 18093 and 18082); drives the session with the `drive-session` scripts | Yes, unless `MODEL_PROVIDER=fake` |

A change meant to make something faster is measured before and after with the same script, arguments and machine.
