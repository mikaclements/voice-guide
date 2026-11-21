# Contributing

How voice-guide is built, in plain words. Setting up, the commands and where things are come first. The rules follow, in numbered sections. [docs/reference/contributing.md](docs/reference/contributing.md) has the same numbered sections in more detail, with the reasoning behind each engineering decision, and it wins where the two differ.

<a id="setup"></a>
### Set up for development

1. Install:
   - Go (the version in `go.mod`);
   - [golangci-lint](https://golangci-lint.run) (the version in `.golangci-lint-version`);
   - Node with npm (the version in `.node-version`);
   - [sqlc](https://sqlc.dev) (the version in `.sqlc-version`: `go install github.com/sqlc-dev/sqlc/cmd/sqlc@v$(cat .sqlc-version)`);
   - [uv](https://docs.astral.sh/uv/), which installs the Python the speech-to-text service pins;
   - Docker, Python 3 and OpenSSL.
2. Clone the repository, turn on the shared git hooks, and run every check CI runs, including the tests against PostgreSQL:

   ```sh
   git clone https://github.com/mikaclements/voice-guide.git
   cd voice-guide
   git config core.hooksPath .githooks
   npm ci
   scripts/check.sh
   ```

<a id="commands"></a>
### Commands

| Command | What it does |
|---|---|
| `scripts/check.sh` | Every check CI runs: formatting, docs, lint with module boundaries, dead code, generated code, Go tests with the race detector and the database, TypeScript, Python |
| `scripts/format.sh` | Formats every Go, TypeScript, JSON and Python file (`--check` only reports) |
| `scripts/up.sh` | Starts everything in Docker, needing nothing else installed: the database, speech-to-text, Kokoro, the model service (Claude API or `MODEL_PROVIDER=fake`), the Go service and the web page at http://127.0.0.1:5173, on a database of its own (`voice_app`). Makes `.env` if missing and asks for a missing key, as `scripts/run.sh` does. Ctrl-C stops it; `scripts/up.sh down` removes the containers and keeps the data |
| `scripts/run.sh` | Starts everything with the settings in `.env`: the database, speech-to-text, Kokoro (on the Mac's GPU with `KOKORO=mlx`), the model service, the web page at http://127.0.0.1:5173 and the Go service. The first run saves the key and settings given in the shell into `.env`, and asks for a missing key. Ctrl-C stops it |
| `scripts/kokoro-mlx.sh` | Runs Kokoro on the Mac's GPU through Apple's MLX at http://127.0.0.1:8882; `scripts/run.sh` starts it with `KOKORO=mlx`. Needs Apple silicon and uv; installs pinned packages and the model (about 0.9 GB) under `~/Library/Caches/voice-guide/`; delete that folder to remove it |
| `scripts/web.sh` | Serves the web page at http://127.0.0.1:5173 on its own, as `scripts/run.sh` does, to restart the page alone (start it first). It plays the product's server, starting each session with the development product key and the placeholder user's resume |
| `scripts/demo.sh` | Serves the test embedding at http://127.0.0.1:5190 ([Try it embedded](README.md#embedded)) |
| `scripts/schematic.sh` | Serves the schematic at http://127.0.0.1:5180; `check` screenshots every level, folded, unfolded and mid-walk-through, failing on overlaps or cut-off text |
| `scripts/publish.sh` | Publishes changed files in `agents/` as new versions; `VOICE_AGENTS_DIR` names another folder, and `VOICE_WORKSPACE_ID` another workspace, for a product's own agents |
| `scripts/build-client.sh` | Builds the drop-in client into `web/dist-client/` when its sources changed; the Go service serves the new build from the next load |
| `scripts/sqlc.sh generate` | Regenerates the Go code for the queries after a query or a migration changes (`diff` checks it is current) |
| `scripts/migrate.sh` | Applies migrations the database in `.env` hasn't run, without restarting |
| `scripts/product-key.sh create\|list\|revoke ID` | The workspace's product keys. `create` prints a new key, the only time it's shown, since only its hash is kept |
| `scripts/webhook.sh set URL\|rotate\|show` | The workspace's webhook. `set` prints the signing secret the first time, the only time it's shown; `rotate` replaces it |
| `scripts/workspace.sh create NAME` | A new workspace besides the seeded one, so each product has its own agents, keys and webhook. Prints its ID and its first product key, the only time the key is shown; the other scripts take the workspace in `VOICE_WORKSPACE_ID` |

The hooks check and never rewrite. A commit checks the formatting of what is staged and refuses files git ignores. A push runs the docs check, lint, the dead-code check and the tests. CI runs the same on every pull request, with the race detector and the database. The speed numbers in the docs come from the scripts in `.claude/skills/measure/`.

<a id="where-things-are"></a>
### Where things are

| Path | What lives there |
|---|---|
| `cmd/voice/` | The Go service's entry point; wires the modules together |
| `cmd/migrate/`, `cmd/publish/`, `cmd/productkey/`, `cmd/webhook/` | The migration step, publishing agents, product keys and the webhook, for the scripts above |
| `cmd/checkspeak/` | Acts as a browser that talks to an agent, for the speed checks in `.claude/skills/measure/` |
| `cmd/classgen/` | Writes the schematic's type views; its tests check the schematic against the code |
| `internal/conversation/` | Sessions and their turns, in text: each turn's prompt, the model's reply, the HTTP API for agents, sessions and typed turns, and the webhook at a session's end |
| `internal/speech/` | A session's audio connection over WebRTC: the user's turns from their microphone (Silero speech detection, speech-to-text, Smart Turn) and the agent's replies spoken (Kokoro), each with a fake |
| `internal/usercontext/` | Loads what an agent may know about the user and saves what a session learned; imports none of the other modules |
| `internal/agents/` | The published versions of each agent file |
| `internal/api/` | The one router: every route behind its credential check (a product key or a ticket); also serves the drop-in client |
| `internal/ticket/`, `internal/productkey/`, `internal/access/` | Tickets, product keys, and the caller a credential names |
| `internal/jobs/` | Background jobs (summaries, webhook deliveries), claimed from PostgreSQL |
| `internal/pgtest/` | Gives each database test its own freshly migrated database |
| `internal/redacttest/` | Checks, in a test, that a value holding a secret never shows it |
| `api/` | `openapi.yaml`, the OpenAPI description of every route and the webhook, for products to generate their client types from |
| `agents/` | The agents, one Markdown file each: a settings block, then the persona |
| `migrations/` | Database migrations (goose), numbered across modules |
| `web/` | voice-guide's web page (React), using only the public API, and the drop-in client built from its code: `src/client/` is what the drop-in client bundles, `src/page/` the test page, `dev/` the product's server stand-in the dev server mounts |
| `examples/job-board/` | The test embedding: a small job board that puts an agent on its page, as a product would |
| `services/model/` | The model service: one streaming API in front of the model providers (Claude through its API or a Claude subscription, or a fake) |
| `services/stt/` | The speech-to-text service (Python): Moonshine behind OpenAI's realtime transcription API, and the end-of-turn check (Smart Turn) |
| `schematic/` | The schematic's page and its hand-written levels |
| `seed/` | The user the test embedding and the web page start sessions for: their reference and resume |
| `compose.yaml` | The local runtime: PostgreSQL, Kokoro and the speech-to-text service; its `app` profile adds the Go service, its setup, the model service and the web page, built by `Dockerfile` |
| `CLAUDE.md`, `.claude/` | The maintainer's setup for AI coding assistants: instructions, skills and hooks. The working notes they mention are kept outside the repository |

## 1. Principles

- Use what's standard, in its simplest form.
- Measure before making anything faster.
- Every decision says what checks it.
- No abstraction unless a second implementation is actually planned; storage is the exception, always behind a port.
- Every line has an owner who can explain it, including code an AI wrote.

## 2. Style and formatting

- Run `scripts/format.sh`; the git hooks and CI check it.
- Use the terms from the design, with no synonyms.
- Comments explain why, in plain words, never cite doc labels, and cite only official standards or regulations, never other products or drafts.

## 3. Go guidelines

- Small interfaces, declared where they're used.
- `context.Context` comes first and is never stored.
- Errors are always handled and wrapped with what was being done.
- No global state; everything is passed in at startup.
- A program checks what it depends on when it starts, with a deadline, so a missing database stops it with a message instead of hanging.
- Code that deals with time takes a clock, so tests never sleep.

## 4. Sync, async and streaming

Every piece of work is one of four kinds:

| Kind | When | Example |
|---|---|---|
| Synchronous | The caller needs the answer now, and it's quick | Listing agents |
| Streamed | The result arrives in pieces | Audio, speech-to-text, the model's reply |
| Concurrent within a turn | It can run alongside other work in the same turn | The model call during the end-of-turn check |
| Background job | Nobody should wait for it | Saving what a session learned |

- Every goroutine has an owner that stops it and waits for it, including the ones a library starts.
- Channels are bounded.
- Cancelling is a normal way to stop.
- Anything that must survive a restart goes in the database, never only in memory.

## 5. Modules and hexagonal layout

- Four modules with walls between them: conversation, speech, user context and agents. Speech calls conversation, never the reverse.
- Inside each, the core holds the rules and depends on nothing outside it but the modules it may call and who a request comes from; HTTP, WebRTC, speech, the model and the database plug in as adapters in subfolders, and a new one is held to its core's limits until it gets its own.
- Ports are for I/O only (storage, the network, vendors); the rules are called directly, never through an interface. A vendor or another service gets a port only where a second real implementation is planned; the one exception so far is the poster that sends deliveries to a product's server, kept so the network stays out of conversation's tests.
- Every port has a fake from its first day.
- Only the programs in `cmd/` wire things together: `cmd/voice` the Go service, `cmd/publish`, `cmd/productkey`, `cmd/webhook` and `cmd/checkspeak` what each needs.
- A vendor's package is used only by its adapter. A `cmd/` program that plays the browser for a check, `cmd/checkspeak`, may use WebRTC's (Pion) itself: it is the other side of the connection, and ships in no service.
- The API's router, the ticket check, product keys and who a request comes from sit outside the modules and depend on none of them, so any module's HTTP part plugs into the router.

## 6. API

- Every endpoint is designed for an outside product to call, and voice-guide's own web page uses nothing else.
- HTTP for agents, sessions and typed turns, whose reply streams back as it is written; WebRTC for audio.
- Requests that create something carry the caller's ID, so a repeat does no harm.
- A product's server calls with its product key, and the user's browser with the ticket voice-guide signed for its session; every route checks one, a route the browser calls refuses a key, and a product's route refuses a ticket. A page on any site may call a route the browser calls, and none may call a product's route or send a product key anywhere; the drop-in client's script is the one thing served with no credential.
- Errors use the standard problem details format, and every path starts with `/v1/`.
- `api/openapi.yaml` describes every route and the webhook, for products to generate their client types from. A change to a route or its body updates it in the same pull request; a test fails when its routes and the router's differ, and review checks the bodies.

## 7. Persistence

- PostgreSQL, with SQL checked by sqlc and migrations run by goose.
- UUIDv7 IDs.
- Every record carries its workspace, and every record about a user carries the user too. Every query filters by workspace, except a few system sweeps and the product key lookup, which the [reference](docs/reference/contributing.md) lists.
- Conversation's tables, the shared workspaces and users, the job queue and the product keys are in the default schema; another module's tables sit in its own schema, named for it in the singular (`agent`); table names are plural.
- Only store adapters touch the database.
- An applied migration is never edited. Migrations run once per deploy, never by the service itself.
- sqlc's generated code is committed and never edited; `scripts/sqlc.sh generate` rewrites it.

## 8. Testing

- The core is tested with fakes: no network, no real vendors, no sleeping.
- Every failure mode in the design gets a test.
- Failure modes are found before writing code, by walking each flow through a fixed list: several copies running, a crash mid-step, a dependency slow or full, repeats, races, extreme settings, the user acting mid-flow, order in time.
- Fix what breaks the obvious path, ordinary use or security while building; extreme edge cases wait for the hardening phase's thorough search. One review round per pull request, and run the feature end to end as soon as it lands.
- One behaviour per test, named for the rule.
- Tests read on their own: helpers named for what they build, example data invented.
- What lives in the database is tested against a real PostgreSQL, each test on its own fresh database; `scripts/check.sh` runs these tests locally, and CI always does.
- A speech adapter is tested against a recording of its vendor's real output; a live test against the real vendor runs by hand through the scripts in `.claude/skills/measure/`, never in CI.
- Python is tested with pytest, and linted and formatted with Ruff.
- CI runs everything under the race detector.
- Never change a test just to make code pass.

## 9. Logging and timing

- Structured logs with `log/slog`.
- Never log what a user said, what's known about them, or a credential.
- An error from a vendor keeps its status and a fixed reason, never its response body.
- Per-stage timing, speech's stages included, is stored with each turn: times only.

## 10. Security and privacy

- No personal data in the repository.
- Secrets come from the environment.
- A vendor's process gets only the environment it needs, never the service's secrets.
- Every request is checked against its workspace and user.
- A provider only ever sees what the agent declares.

## 11. Dependencies

- The standard library first.
- Every new dependency is agreed with a maintainer before it's added.
- Each version lives in one place.

## 12. Version control

- Small commits in Conventional Commits form.
- One idea per pull request, planned at 300 to 600 lines and never past 800; lockfiles, generated code and the schematic's files don't count, and docs-only work is exempt.
- Work that builds on an open pull request is a GitHub stack, merged bottom up. When something below it changes, the stack is rebased and pushed with `--force-with-lease`, never updated with merge commits.
- A pull request's description reads in a minute: a summary that opens with how things work before and after the change, each with a small example, then every change with its why; a diagram, the key files, and notes. Every number chosen, such as a timeout or a limit, says where it comes from, even when it's a guess.
- The hooks are never skipped.
- A maintainer merges after review; merged branches stay.

## 13. Workflow

- Agree a change's design before writing its code.
- Update every affected doc in the same change.
- Anything that isn't one plain command gets a script: in `scripts/` if people run it, `scripts/lib/` if only other scripts, the hooks or CI do, or its skill's folder in `.claude/skills/` if only AI coding assistants do.
- Watch every new check fail once before trusting it.
- A change to a part, a port, a connection between parts or a design decision updates the schematic in the same pull request, its walk-throughs included; the tests fail on a package, an import or a port's method it doesn't show, and on a walk-through step naming a card or connection that isn't there.
- When review raises a rule, it goes into the standards in the same pull request.
- The repository carries instructions for AI coding assistants beside the code: `CLAUDE.md` and skills in `.claude/skills/` (such as `verify`, `measure` and `schematic`) that run the project's checks, measurements and upkeep. They follow the same rules as everyone else, and "agent" in these docs always means the AI a user talks to.

## 14. Engineering decisions

The main ones: one formatting script; golangci-lint with module boundaries as errors; no dead Go code; the Go service at the root with other services beside it; adapters in folders by role; hooks that check and never rewrite; CI with pinned actions and images; small pull requests; a docs check on every push; database tests against a real PostgreSQL; and a schematic of the system kept with the code. Each is in the [reference](docs/reference/contributing.md#decisions) with its reasons and what checks it.
