# Engineering standards

How code in voice-guide is written, tested and reviewed. The detailed reference, written for agents: every rule, and the engineering decisions (E) behind them. The short version for people is [CONTRIBUTING.md](../../CONTRIBUTING.md), with the same sections; where the two differ, this document wins. What the system does and why is in the design, [architecture.md](architecture.md).

The git hooks and CI enforce what a tool can check: formatting, static checks, module boundaries, tests and the docs check. Everything else is checked in review.

<a id="principles"></a>
## 1. Principles

- Standard first, then simplest: use what reputable Go projects use, in its simplest form that works.
- Measure before optimising: speed work follows per-stage timing ([D13 per-stage timing](architecture.md#d13)).
- Every decision names what enforces it: a test, a lint rule, a database constraint, a hook or a deny rule, or "guidance only" with the reason.
- No abstraction without a second implementation in view. Swap points are interfaces because two implementations are planned for each, and storage is always behind a port, since the database sits outside a core like any vendor ([D2 swap points](architecture.md#d2)); nothing else is.
- Every line has an owner who can explain it, including code an AI wrote.

<a id="style"></a>
## 2. Style and formatting

- Formatting is `scripts/format.sh`, which runs `golangci-lint fmt` (gofmt, goimports) on Go, Biome on TypeScript and JSON, and Ruff on Python, for people, the editor hook, the git hooks and CI ([E1](#e1)). Never an IDE's own formatter.
- Names use the design's terminology ([Appendix A](architecture.md#appendix-a)), with no synonyms. Package names are short, lower case, single words with no underscores (`usercontext`). Names don't repeat their package (`conversation.Turn`, not `conversation.ConversationTurn`).
- Comments explain why, sparingly, in their own words. They never cite doc labels or section numbers, which get renumbered, and never sit inside a call chain. They may cite an official standard or regulation (an RFC, a W3C specification, a law) whose rule the code follows, never another product, project or draft as precedent: the reason is given in its own words instead.

<a id="go"></a>
## 3. Go guidelines

- Interfaces are small and declared in the package that uses them. Functions accept interfaces and return concrete types.
- No dead code: a function nothing calls, or one in production code only tests call, is deleted or moved into a fake; `scripts/lib/deadcode.sh` checks it ([E15](#e15)).
- `context.Context` is the first parameter of anything that does input or output or may block. It is passed down, never stored in a struct.
- Errors are returned, never ignored, and wrapped with what was being done (`fmt.Errorf("load agent %s: %w", id, err)`). Callers compare with `errors.Is` and `errors.As`. A panic is only for a programmer error found at startup.
- No package-level mutable state and no `init` side effects. Every dependency is passed in from the program in `cmd/` that runs the code.
- A program checks what it depends on when it starts, each check with a deadline, so an unreachable database stops it with a message instead of hanging.
- Code that measures or waits on time takes a clock as a dependency, so tests control time instead of sleeping.

<a id="concurrency"></a>
## 4. Sync, async and streaming

Every piece of work is one of four kinds. Pick the kind first; the rules follow from it.

| Kind | Use when | Examples | Rules |
|---|---|---|---|
| Synchronous | The caller needs the result before it can go on, and the work is short | Listing agents; creating a session record; reading an agent version | Bounded by a deadline on its context; nothing slow inside it |
| Streamed | The result arrives in pieces over time | Audio in and out; speech-to-text results; the model's reply; text-to-speech audio | A stage reads from an input channel and writes to an output channel, and starts on the first piece |
| Concurrent within a turn | It can run alongside other work in the same turn, and the turn needs its outcome | The model call during the end-of-turn check ([D30](architecture.md#d30)); saving the user's words during the model call | Started and waited for by the turn; cancelled when the turn is |
| Background job | The user shouldn't wait for it at all | Saving what a session learned; compacting a long conversation | Durable, retried and safe to repeat; never inside a request or a turn ([D15](architecture.md#d15)) |

The turn's pipeline in the design is the worked example ([section 9](architecture.md#pipeline)).

Rules for every kind:
- Every goroutine has an owner that starts it, cancels it through its context, and waits for it to finish. No goroutine outlives the turn or session that started it. A vendor's goroutines count: close what it opened with the call that waits for them (Pion's `GracefulClose`, not `Close`).
- Channels between stages are bounded, so a slow stage slows the one before it instead of piling up memory. Work ahead is bounded too, so an interruption wastes little.
- Cancelling is the normal way a stage stops: on an interruption, on a speculative request that turned out wrong, or when the session ends. Every stage stops promptly when its context is cancelled and releases what it holds.
- Nothing that must survive a restart lives only in a goroutine or a channel; it is in the database or a background job ([D14](architecture.md#d14)).
- Ordering within a session is explicit: one turn in flight, and a turn saved before the next prompt is built.

<a id="modules"></a>
## 5. Modules and hexagonal layout

- conversation, speech, user context and agents are modules under `internal/` with fenced boundaries; speech imports conversation, never the reverse ([D1](architecture.md#d1)), checked by depguard lists in `.golangci.yml` ([E3](#e3)).
- Inside each module the shape is hexagonal ([D31](architecture.md#d31)). The core sits in the module's folder, holds the rules, and declares its ports as small interfaces; it imports nothing outside the module except what its list allows. Adapters live in subfolders by role and implementation ([E5](#e5)).
- A port is only for I/O. Storage always gets one, so a core never touches the database directly; a vendor or another service gets one where a second real implementation is planned, and a fake never counts as that second implementation. One exception so far: the webhook poster, a port with one real implementation, kept so the network stays out of conversation's tests while the core signs ([D40](architecture.md#d40)). Domain logic is called directly, never through an interface ([D2](architecture.md#d2)).
- Every port has a fake adapter from its first day, used by the core's tests and by load tests ([D2](architecture.md#d2)).
- Only the programs in `cmd/` wire adapters to a core and modules to each other: `cmd/voice` for the service, and `cmd/publish`, `cmd/productkey`, `cmd/webhook` and `cmd/checkspeak` for what each needs.
- A vendor's package is imported only by its adapter, never by a module's core. The exception is a program in `cmd/` that plays the browser for a check, as `cmd/checkspeak` does for `.claude/skills/measure/check-speak.sh`: it may import the transport's vendor (Pion) itself, since it is the other side of the connection, which no adapter of voice's plays, and it ships in no service.
- The API's router (`internal/api`), the ticket check (`internal/ticket`), product keys (`internal/productkey`, whose store is an adapter of its own) and the caller a credential names (`internal/access`) sit outside the modules and import none of them, so any module's HTTP adapter registers on the router without importing another module.
- A depguard list changes only in the pull request that needs it, so every loosening is visible in review.

<a id="api"></a>
## 6. API

- Every endpoint and message is designed for a product outside voice-guide to call; voice-guide's own web page uses only that API ([D29](architecture.md#d29)).
- HTTP for agents, for starting and ending sessions and for typed turns, the reply streamed as server-sent events; WebRTC for audio ([D11](architecture.md#d11)).
- Requests that create something carry an ID chosen by the caller, so a repeat has the effect of one ([NFR5 safe retries](architecture.md#nfr5)):
  - A request refused because another is in flight (409, or `busy` on the data channel) wasn't taken: the caller puts it back and sends it again as a new request with a new ID, unless it was itself a retry of a request that may have been taken, whose first try may be the one in flight.
  - Only a request that may have been taken (a server error, a dropped connection, `failed`) is sent again with the same ID.
  - A repeat of a request already done is acknowledged and not done again: a repeated turn is never answered or spoken a second time, and its text shows at most once.
- A product's server calls with its product key, and the user's browser with the ticket voice-guide signed for its session, handed back by the start ([D32](architecture.md#d32)); routes are registered only through `internal/api`, the one router every module's HTTP adapter registers on, which adds the check, so none can be added without it: `HandleProduct` for a route a product's server calls, which refuses a ticket; `Handle` for a route in one session, its ID in the path as `{id}`, which the browser calls, which refuses a product key unread and answers another session's ticket 404; and `HandleSessionEnd` for the session's end, which also takes the ticket shortly past its lifetime and the product's key. The routes `Handle` and `HandleSessionEnd` register answer a page on any site and those `HandleProduct` registers none; a product key sent with an `Origin` header is refused unread on every route; the router answers a preflight from the route alone, never running a handler or reading a credential, and serves the drop-in client, its one route with no credential, outside `/v1/` ([D41](architecture.md#d41)).
- Errors are problem details and the version is in the path ([D33](architecture.md#d33)).
- The published description of the API comes with embedding, when products outside voice-guide first build against it ([W3](architecture.md#w3)).

<a id="persistence"></a>
## 7. Persistence

- PostgreSQL 18 ([D5](architecture.md#d5)). Queries are written in SQL and checked by sqlc, run with pgx; migrations are plain SQL files run by goose ([D6](architecture.md#d6)).
- IDs are UUIDv7 ([D7](architecture.md#d7)).
- Every stored record carries its workspace ID, every record about a user carries the user's ID, and every query filters by workspace, except the system's own sweeps, ending idle sessions and claiming and deleting background jobs, which find their rows by one condition across workspaces, the same for every workspace; ending and deleting read nothing out, and the claim reads out only the job it takes; and the product key check, which finds a key by its hash, since the key is what names the workspace ([D12](architecture.md#d12)).
- Conversation's tables (`user_references` and `webhooks` among them), the records every module points at (`workspaces`, `users`), the job queue (`jobs`, owned by the job runner, `internal/jobs`, which a module writes to only to queue a job of its own kind) and the product keys (`product_keys`, owned by `internal/productkey`, outside the modules) are in the default schema; any other module that keeps tables keeps them in its own schema, named for the module in the singular (`agent`, `user_context`); table names are plural. A schema marks which module owns a table; it isn't a lock, since the service connects with one database login, so only a module's own store adapter names its schema in a query ([D1](architecture.md#d1)).
- Only a store adapter touches the database ([D31](architecture.md#d31)).
- A migration is never edited once applied. Once anything is released, every migration stays backward compatible with the release before it.
- Migrations live in `migrations/`, numbered across modules, each file changing one module's schema. `cmd/migrate` applies them once per deploy, before new instances take traffic; the service never migrates.
- Each store adapter's queries sit beside it in `queries.sql`; sqlc generates the `db` package from them and the migrations (`scripts/sqlc.sh generate`). The generated code is committed and never edited; CI fails when it isn't current.

<a id="testing"></a>
## 8. Testing

- The core is tested through its ports with fake adapters: no network, no real vendor, no sleeping.
- Every failure mode in a use case of the design gets a test. Decision boxes are test cases too: test what each decides and its trade-off.
- Failure modes are found before code, by walking each flow through a fixed list of scenarios (several copies of the service, a crash mid-step, a dependency slow or at its cap, repeats, races with other paths, settings at their extremes, the user acting mid-flow, order in time); what the design doesn't answer is decided first. Review checks the code against that list, not only the code against itself.
- While a feature is built, flaws on the obvious path, the failures users meet in ordinary use, and security holes are fixed; extreme edge cases (a rare race, a window of a second, a subtlety of a rolling deploy) and low findings are written down for the hardening phase, which searches them thoroughly. One review round per pull request, a second only when a fix touched security, and the feature is run end to end as soon as it lands.
- One behaviour per test, named for the rule it checks, not the example value. Table-driven tests where cases share a shape.
- Tests read on their own. Helpers are named for what they build (`agentFile`, `settings`), never for one agent or use case, and example data is invented for the test, so a reader needs no knowledge of the product's own agents.
- Concurrency is tested by controlling the clock and the order of events, never by waiting and hoping. CI runs every test under the race detector ([E7](#e7)).
- The standard library's `testing` package; a test helper library is a new dependency like any other ([section 11](#dependencies)). Python is tested with pytest and TypeScript with Node's own runner.
- Never change a test just to make code pass. A justified test change is called out in the pull request.
- Anything that lives in the database (tables, constraints, triggers, store adapters) is tested against a real PostgreSQL: each test gets its own freshly migrated database from `internal/pgtest` ([E13](#e13)). `scripts/check.sh` starts the compose database and runs these tests with everything else; a plain `go test` skips them, and CI never does.
- A speech adapter is tested against a recording of its vendor's real output, kept in the adapter's `testdata/` and served by a test server, so its tests need no network and no vendor. A live test against the real vendor skips unless `TEST_<VENDOR>_URL` names one; a `scripts/check-<stage>.sh` starts the vendor in compose and runs it by hand, never in CI, since vendor images are large and their speed depends on the machine.

<a id="logging"></a>
## 9. Logging and timing

- Logs are structured, through the standard library's `log/slog`, written as JSON to standard output.
- Logs never contain what a user said, a context item, or a credential.
- An error from a vendor or another service keeps its status and a fixed reason, never its response body: a body can quote the text it was sent, which holds what the user said, and errors reach logs.
- Per-stage timing is recorded with each turn ([D13](architecture.md#d13)). Speech's stages for a spoken turn are logged until they are stored with it: times only, never what was said.

<a id="security"></a>
## 10. Security and privacy

- The repository holds no personal data, only sample data.
- Secrets and settings come from the environment ([D10](architecture.md#d10)); agents never read `.env` files, which a deny rule enforces.
- A vendor's process that a service starts gets only the environment variables it needs, named one by one, never the whole environment, so the service's own secrets stay with the service.
- Every request is checked against its workspace and user, even with one placeholder user.
- What reaches a model or speech provider is limited to what the agent declares ([D27](architecture.md#d27)).

<a id="dependencies"></a>
## 11. Dependencies

- The standard library first. Every new dependency is agreed with a maintainer before it is added.
- Each version lives in one place ([E8](#e8)). Dependabot proposes updates ([E7](#e7)).

<a id="version-control"></a>
## 12. Version control

- Commits are small and say why, in Conventional Commits form: `type(scope): description`, with `feat`, `fix`, `test`, `refactor`, `docs`, `build`, `ci` or `chore`. Changes to the agent setup are `chore`.
- One branch per pull request, named `type/short-name` with the pull request's type; titles take the commit form. Descriptions follow the template and read in a minute: four sections (Summary, Diagram, Key files, Notes), about 300 words. The summary opens with what happens before the change and what happens after it, each with a small example from someone using it, so a reader sees the difference without reading the code. Each change in the summary, and each choice a reviewer could question, gives its reason after a bold `Why:`; the diagram is plain ASCII in a code block, so it lines up in any font. Every number a change chooses (a deadline, a limit, a default) gets its own `Why:` saying where it comes from: a measurement, the numbers it is derived from, or a round guess and when it will be measured; the comment beside the constant says the same.
- Pull requests stay small and hold one idea each ([E9](#e9)).
- Work that builds on an open pull request is a GitHub stack: it merges bottom up, and GitHub rebases the pull requests above after each merge. When `main` moves or a lower pull request changes, the agent rebases the stack itself and pushes each branch with `--force-with-lease`, never merging one branch into another: GitHub's rebase can't replay a merge commit, so the pull request above would show conflicts after every merge.
- The git hooks run on every commit and push and are never skipped ([E6](#e6)). A maintainer merges; merged branches stay ([E10](#e10)).

<a id="workflow"></a>
## 13. Workflow

- An item's spec is agreed before its code starts. Specs live in the working notes.
- A decision goes into its home doc, and every doc it affects, in the same change (the `sync` skill).
- A task that isn't one plain command gets a script: one people run in `scripts/`, plumbing other scripts, the hooks or CI run in `scripts/lib/`, an agent-only task in its skill's folder (`.claude/skills/<name>/`); docs show the script, not the commands.
- Text-to-speech runs in compose by default, on the CPU. `KOKORO=mlx` in `.env` makes `scripts/run.sh` run Kokoro natively through `scripts/kokoro-mlx.sh` instead, on Apple's MLX: it needs macOS 14 or later on Apple silicon and `uv`, and runs a small server of ours (`services/kokoro-mlx/`) with the API voice uses, its Python packages at fixed versions and hashes (wheels only, so nothing builds from source) and the model at a fixed revision, about 0.9 GB under the user's cache folder on first run (uv's own cache grows too; the code runs on the Mac, outside a container); deleting the folder removes it. It runs offline, on port 8882 (`KOKORO_MLX_PORT`), beside the container's 8880; it is linted with the other Python but not installed or run in CI, since MLX needs Apple silicon. `TEST_KOKORO_URL` points `check-tts.sh` at either, so one script compares them.
- Every gate is seen failing once before it is trusted ([E12](#e12)).
- What lives in the database is tested against a real PostgreSQL, a fresh database per test ([E13](#e13)).
- A pull request that adds, changes or removes a service, a package, a port or its methods, an adapter, a connection between parts or a design decision updates the schematic in the same pull request (the `schematic` skill says how); `go test` fails on a package, an import between the parts of voice or a port's method that it doesn't show ([E14](#e14)).
- When a review comment raises a rule rather than a one-off fix, the rule is added here in the same pull request, and the comment gets a reply saying what changed.

<a id="decisions"></a>
## 14. Engineering decisions

The choices behind the rules above, in a shorter form than the design's decisions. Labels are numbered in the order they were created and never reused. Decisions about the system itself (behaviour, data, concurrency between parts, the technology stack) are in [architecture.md](architecture.md).

<a id="e1"></a>
#### E1. One formatting script
- Decision: `scripts/format.sh` is the one way to format. Go goes through `golangci-lint fmt` with gofmt and goimports, at the version in `.golangci-lint-version`; TypeScript and JSON through Biome, at the version in the root `package.json`, which also lints TypeScript in CI; Python through Ruff, at the version in the service's `uv.lock`, which also lints Python (`scripts/lib/check-py.sh`).
- Needed by: [section 2](#style).
- Why: gofmt is the Go standard; Biome formats and lints TypeScript in one tool, and Ruff does the same for Python; one script and one version of each for people, the editor hook, the git hooks and CI.
- Rejected: gofumpt (stricter, less common); ESLint with Prettier (two tools, though the larger install base); Black with Flake8 (two tools); an IDE's own formatter.
- Status: accepted.
- Enforced by: the pre-commit hook and CI; the script fails when Biome or uv isn't installed instead of skipping TypeScript or Python.

<a id="e2"></a>
#### E2. Static checks
- Decision: golangci-lint's standard set of linters plus depguard, every finding an error. More linters are added when a need shows.
- Needed by: [section 3](#go); [section 5](#modules).
- Why: the standard set catches real bugs with little noise; findings as errors, because warnings are ignored once there are many.
- Rejected: a large linter set from the start.
- Status: accepted.
- Enforced by: the pre-push hook and CI.
- Revisit: when the same review comment comes up twice and a linter could catch it.

<a id="e3"></a>
#### E3. Module boundaries through `internal/` and depguard
- Decision: modules live under `internal/`; each core folder has a depguard allow-list in golangci-lint, so an unlisted package, another module or a vendor, fails the lint; any other package under `internal/` without a list of its own falls under one that allows only the standard library.
- Needed by: [section 5](#modules); [D1](architecture.md#d1).
- Why: golangci-lint is the lint gate anyway, and with few modules per-folder lists stay readable.
- Rejected: go-arch-lint (one more, less common tool); a custom import test (more code to own).
- Status: accepted.
- Enforced by: the pre-push hook and CI; each list was broken once on purpose and seen failing.
- Revisit: when modules multiply and the lists stop reading clearly.

<a id="e4"></a>
#### E4. Repository layout
- Decision: the Go service at the root (`go.mod`, `cmd/`, `internal/`); agent files in `agents/`; invented sample data for development and tests in `seed/`; each other service in `services/<name>/`; the React app in `web/`; `compose.yaml` at the root. `go.mod`'s `ignore` keeps `node_modules` out of `./...`.
- Needed by: [section 5](#modules).
- Why: Go's own layout for a server; Go tools run from the root; no import paths that repeat `voice`.
- Rejected: every service in its own folder, Go included; `apps/` and `packages/` with Turborepo or Nx.
- Status: accepted.
- Enforced by: guidance only; the folders are visible in every review.

<a id="e5"></a>
#### E5. Adapter folders
- Decision: inside a module, adapters live in subfolders named by role, then implementation: `http/`, `transport/webrtc/`, `stt/moonshine/`, `tts/kokoro/`, `store/postgres/`. A fake is an adapter like any other, in its own folder (`stt/fake/`), so tests and load tests use the same one.
- Needed by: [section 5](#modules); [D31](architecture.md#d31).
- Why: a reader finds every implementation of a port in one place, and a swap adds a folder beside the others.
- Rejected: one `adapters/` folder for everything; fakes only inside test files, where load tests can't reach them.
- Status: accepted.
- Enforced by: depguard lists per folder; a core's list leaves out only the adapter folders with a list of their own, so a new adapter folder is held to its core's list until it gets one.

<a id="e6"></a>
#### E6. Git hooks that check and never rewrite
- Decision: shared hooks in `.githooks/`, turned on with `git config core.hooksPath .githooks`. Pre-commit refuses files git ignores and checks the formatting of what is staged, which is what the commit carries; pre-push runs the docs check, the lint, the unit tests, the TypeScript checks (`scripts/lib/check-ts.sh`) and the Python checks (`scripts/lib/check-py.sh`).
- Needed by: [section 12](#version-control).
- Why: problems surface before they leave the machine, and a hook that changes files would hide what is being committed.
- Rejected: hooks that format and re-stage; skipping hooks with `--no-verify`.
- Status: accepted.
- Enforced by: the hooks themselves; CI repeats every check; deny rules stop the usual ways an agent could skip them (`--no-verify` and its abbreviations, `git commit -n`, `git -c core.hooksPath=...`, `--config-env`), though not every form git accepts (bundled short flags, other letter cases), which is why CI repeats every check; `scripts/check.sh` warns when a clone hasn't turned them on.

<a id="e7"></a>
#### E7. CI and dependency updates
- Decision: CI runs the hooks' checks on every pull request and push to main, with the tests under the race detector. Actions are pinned by commit SHA and compose images by digest, the workflow token is read-only, and checkout keeps no credentials. Dependabot opens one grouped pull request a week per ecosystem, minor and patch versions only; the text-to-speech image is left out, since a new build is measured first.
- Needed by: [section 8](#testing); [section 11](#dependencies).
- Why: the same checks hold even when a hook was skipped; pinned actions and images can't change under us; majors are deliberate upgrades.
- Rejected: actions pinned by tag, which can be moved.
- Status: accepted.
- Enforced by: the workflow file; CI was seen failing once on a boundary break.

<a id="e8"></a>
#### E8. One home per version
- Decision: Go's version lives in `go.mod`; golangci-lint's in `.golangci-lint-version`; Node's in `.node-version`; sqlc's in `.sqlc-version`; the JavaScript tools' in the root `package.json` and its lockfile; Python's in `services/stt/.python-version`, uv's in that service's `pyproject.toml` (`required-version`, which uv checks wherever it runs, the image's build included), and the Python packages' in `pyproject.toml` and `uv.lock`; Kokoro on MLX's Python and packages in `services/kokoro-mlx/` (`.python-version`, `requirements.in` and the `requirements.txt` compiled from it). CI and the README read them from there.
- Needed by: [section 11](#dependencies).
- Why: a version written in two places drifts once one of them is updated.
- Rejected: versions repeated in the workflow and the README.
- Status: accepted.
- Enforced by: CI reads each version from its file, so a mismatch can't exist.

<a id="e9"></a>
#### E9. Small pull requests, one idea each
- Decision: a pull request is planned at 300 to 600 lines and never goes past 800, added plus removed, tests and docs included; the item's spec estimates each one, and one that would pass 800 is split by idea instead; work that is only docs is exempt, and files a tool writes don't count: lockfiles (`package-lock.json`, `go.sum`) and the code sqlc generates, whose queries and migrations are what gets reviewed. The schematic's files (`schematic/`, `cmd/classgen/`) don't count either, whatever their size, whether in the pull request that builds it or in any that updates it ([E14](#e14)). One under 50 lines is considered for merging into a neighbour. One idea per pull request, but none that only prepares for another: a tool ships with what it acts on.
- Needed by: [section 12](#version-control).
- Why: one sitting of careful review; a reviewer sees whole changes, not fragments. Planning at 600 leaves room for what a build finds, so the ceiling is never crossed; pull requests of 2,000 lines and more took three or four review rounds each, where ones of a few hundred lines settled in one.
- Rejected: planning at 800 (more than one sitting); a hard cap at the planned size (a build that finds one more failure path would cross it, or be split mid-idea); splitting a checker from the files it checks; counting lockfiles (one new package can add hundreds of lines nobody reviews line by line); counting the schematic (exempt: most of it is a page and its data, read on screen rather than line by line, and splitting a change to the system from the drawing of it would let the two drift).
- Status: accepted.
- Enforced by: guidance only; each pull request reports its real size, generated files shown apart. `.gitattributes` marks them as generated, so GitHub collapses them in the diff.

<a id="e10"></a>
#### E10. A maintainer merges; branches stay
- Decision: only a maintainer merges, with the button. Merged branches stay on GitHub. Agents never merge, delete a branch on GitHub, push to `main` or change remotes. They force-push only the branches of their own open pull requests, and only with `--force-with-lease`, to keep a stack's history straight.
- Needed by: [section 12](#version-control).
- Why: a human approves every change, and the history of every branch stays visible.
- Rejected: automatic deletion of merged branches; no force-push at all, with stacks kept up to date by merge commits, which left the pull request above conflicting after each merge.
- Status: accepted.
- Enforced by: deny rules in `.claude/settings.json`, which hold in every permission mode: merging, deleting branches (`--delete` or `-d`, a `:branch` refspec, `--prune`, `--mirror`, and the abbreviations git accepts for them), pushing to `main`, a bare `--force`, `-f` or a `+` refspec, and changing remotes. `--force-with-lease` refuses to overwrite anything the agent hasn't seen.

<a id="e11"></a>
#### E11. A docs check on every push
- Decision: `scripts/lib/check_docs.py` checks links, labels, decision boxes, the decision index, coverage, the doc pairs and plain ASCII, on push and in CI. Labels are never renumbered; `--fix-index` rebuilds the index in reading order.
- Needed by: [section 13](#workflow).
- Why: docs that drift mislead every agent that reads them; stable labels keep links and history intact.
- Rejected: renumbering labels in reading order, which broke links and lost the history of reversed decisions.
- Status: accepted.
- Enforced by: the pre-push hook and CI.

<a id="e12"></a>
#### E12. Every gate seen failing once
- Decision: a new check is broken on purpose and seen failing, then seen passing, before it is trusted; the pull request that adds it shows both.
- Needed by: [section 13](#workflow).
- Why: a check that has never failed may not run at all.
- Rejected: trusting a check because it passes.
- Status: accepted.
- Enforced by: guidance only; the Checked note in each pull request that adds a check.

<a id="e13"></a>
#### E13. What lives in the database tested against a real PostgreSQL
- Decision: tests of tables, constraints, triggers and store adapters run against PostgreSQL itself, the same version as `compose.yaml`. Each test gets its own database, copied from a template that has every migration applied, and dropped when the test ends. Locally the compose database serves them through `scripts/check.sh`; in CI the same compose database does, started by the job. Without `TEST_DATABASE_URL` a plain `go test` skips them with a message; in CI they fail instead of skipping.
- Needed by: [section 8](#testing); the design's rule that every query filters by workspace, but for the system's own sweeps and the product key check, tested per query.
- Why: what storage must guarantee lives in the database (a trigger, a row lock, constraints), and only the real thing shows it holding. A template copy takes milliseconds, so tests stay fast and never see each other's rows.
- Rejected: testcontainers-go (starts containers from Go, but brings a large dependency tree); one shared database cleaned between tests (tests could no longer run in parallel); an in-memory imitation of PostgreSQL (it would test the imitation).
- Status: accepted.
- Enforced by: `internal/pgtest`, which fails these tests in CI when no database is set; the CI job's compose database.

<a id="e14"></a>
#### E14. A schematic of the system, kept with the code
- Decision: a local page, `schematic/`, shows voice-guide level by level: the system, the parts inside voice, each module's core, ports and adapters, then the types in its code. Each of the top three levels opens on its main parts, with a module's adapters folded behind the ports they implement until clicked, and tells two to four walk-throughs: named stories, such as a turn or a session's end, that the reader steps through or plays, each step lighting the cards and connections involved under one plain caption. Every connection is labelled with what flows over it, shown when it is pointed at or lit; a card says in one sentence why it is built that way, with the design decisions behind it, folded away, linking to the design. The top three levels are written by hand from the design and the code; the types are generated by `cmd/classgen` from Go's type checker each time `scripts/schematic.sh` runs, and never committed. The change that alters a part, a connection or a decision updates the schematic in the same pull request, and its files don't count toward the size limit ([E9](#e9)).
- Needed by: [section 13](#workflow).
- Why: someone new to voice-guide sees how its parts connect, and why, from the services down to the types, before reading the code; a drawing kept by hand drifts unless a check fails when it does.
- Rejected: diagrams-as-code tools such as LikeC4 and Structurizr (no class shape at the lowest level); Mermaid in the docs (no drilling down, little control over layout); a schematic generated from the code alone (no labelled flows, no decisions); every decision listed beside every level (read as a wall of titles, rarely as help).
- Status: accepted.
- Enforced by: `cmd/classgen`'s tests in `go test`, which fail on a package the schematic doesn't name or that sits inside no part of voice, a package, file or path it names that no longer exists, an import between two parts of voice with no edge between them, and a port whose drawn methods differ from its interface's; the schematic's tests, which fail on a reference to a card, view or decision that doesn't exist, a decision the design no longer has, a walk-through step naming a card or connection the level doesn't have, or a level with cards and no walk-through; `scripts/schematic.sh check` by hand, for overlaps and cut-off text, folded, unfolded and mid-walk-through.

<a id="e15"></a>
#### E15. No dead Go code
- Decision: `scripts/lib/deadcode.sh` runs Go's `deadcode` twice and fails on what it finds: with the tests as roots, any function nothing calls; with only the programs in `cmd/` as roots, any function outside the fakes and `internal/pgtest` that only tests reach, since production code kept alive by its own tests is dead too. Its version is the `tool` line in `go.mod`.
- Needed by: [section 3](#go).
- Why: golangci-lint's `unused` sees only names a package doesn't export, so an exported function nobody calls, or one only a test calls, passes it; `deadcode` follows calls from the programs across packages.
- Rejected: staticcheck's unused check (the same per-package limit); a review-only rule (dead code slips in one function at a time).
- Status: accepted.
- Enforced by: `scripts/check.sh`, the pre-push hook and CI.
