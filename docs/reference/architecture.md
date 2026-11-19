# Design: voice-guide

The detailed reference, written for agents: every use case, decision, failure mode and open question, with labels that other docs cite. The short version for people is [ARCHITECTURE.md](../../ARCHITECTURE.md), with the same sections and the diagrams; where the two differ, this document wins.

<a id="overview"></a>
## 1. Overview

voice-guide is a platform that products embed to give their users AI agents they can talk to. One service holds:
- voice, which gives an agent a voice: speech in and out, turn-taking, interrupting, per-stage timing. It is two modules: conversation runs sessions and their turns in text, and speech handles the audio around it.
- User context, which loads what the agent may know about the user for a session and saves what the session learned.
- Agents, each holding its own persona, its activity (how a session runs) and its voice settings, so one platform runs any number of agents.

Who depends on it:
- Products add voice-guide to their own pages, with their own agents ([W3 put an agent on a product](#w3)): a product's server starts each session with its key, the user's browser talks, and the product's server hears each session once it has ended ([D40](#d40)); voice-guide's own web page is the first.
- Users talk to an agent in a desktop browser, by voice or by typing, and expect it to answer quickly and to remember them across sessions.
- Members of a workspace create and change the workspace's agents ([W1 create or change an agent](#w1), excluded for now).
- The team running voice-guide watches where each turn's time goes and runs load tests ([O1 timing and load tests](#o1)).

In short:
- Products reach voice-guide only through its API, and its own web page uses that same API ([D29 an API for outside products](#d29)).
- One Go service holds four modules with enforced boundaries: conversation and speech (together, voice), user context and agents ([D1 modules](#d1)).
- Speech runs on voice-guide's own servers: the browser sends audio over WebRTC, and each speech stage (noticing speech, speech-to-text, end of turn, text-to-speech) sits behind its own swap point ([D2 swap points](#d2), [section 9](#turns)).
- The prompt is layered and cached, so a turn sends the model only what is new ([D24 layered prompt](#d24)).
- Every turn records how long each stage took; the slowest stage is worked on first ([D13 per-stage timing](#d13)).
- What an agent learns about a user belongs to the workspace holding it, and an agent reads only the kinds of context it declares ([D3 ownership](#d3), [D27 declared context](#d27)).
- Built for one user on one machine; designed to grow to many users without early work blocking a later stage ([NFR2 scalability](#nfr2)).

Reading this document:
- Sections 2 to 6 cover the system as a whole: why it exists and what it is, how it works, scope, qualities, and what every part shares.
- Sections 7 to 12 cover one feature each. Every feature section has the same parts in the same order, leaving out any it doesn't need, so each can be read on its own:
  - overview
  - requirements
  - qualities
  - assumptions
  - API
  - how it works
  - use cases
  - data model
  - limitations and open questions
- The appendices hold the terminology, the conventions this document follows, and an index of all decisions.

How code is written, tested and delivered is in the standards, [contributing.md](contributing.md); how to run the service is in the README.

<a id="purpose"></a>
## 2. Purpose

voice-guide lets any product give its users AI agents they can talk to out loud, as naturally as with a person, and that remember them from one conversation to the next. A product adds voice-guide to its own pages; voice-guide handles speech, turn-taking, sessions and what is known about the user, so the product only defines its agents and what is on its screen.

<a id="problem"></a>
### 2.1 Why it's needed

Without it, each product that wants a voice agent rebuilds those parts on its own, and the copies drift apart. Each agent knows only its own conversations, so what is known about a user splits into diverging copies. Browser speech recognition ties an agent to one browser and one language and delivers final text late, and the usual speed fix, a model process held open per session, can't spread across servers.

<a id="what-it-is"></a>
### 2.2 What it is

- Embeddable: a product adds voice-guide to its own pages through a public API, and voice-guide never needs to know what the product is ([NFR9 embeddability](#nfr9)).
- Conversational: replies start quickly, the user can interrupt, and it knows when the user has finished ([NFR1 latency](#nfr1), [U3 interrupt the agent](#u3)).
- Continuous: an agent knows the user from earlier sessions and from the workspace's other agents ([U8 another agent knows the user](#u8)).
- Private by default: an agent sees only what it declares, and what a workspace learns stays there unless the user shares it ([NFR3 privacy](#nfr3), [D3 ownership](#d3)).
- Agent-neutral: it decides how a conversation flows, never what is said, so any agent runs on it unchanged ([D1 modules](#d1)).
- Measured: every turn is timed by stage, and speed work goes where the time goes ([D13 per-stage timing](#d13)).
- Replaceable parts: no model or speech vendor is built in ([NFR8 swappability](#nfr8)).
- Grows without rework: it runs on one machine today, and nothing in it blocks many users ([NFR2 scalability](#nfr2)).

<a id="what-it-isnt"></a>
### 2.3 What it isn't

- An agent: it has no subject or persona of its own.
- A text chat with voice added: speech is the main way in, and typing is the fallback.

<a id="how"></a>
## 3. Scope

<a id="use-cases"></a>
### 3.1 Use cases

The use cases are the functional requirements. Each one's full text is in its feature.

| Feature | Use case | Caller | Scope |
|---|---|---|---|
| [Sessions](#sessions) | [U1](#u1) Start a session with an agent | User | Included |
| [Turns](#turns) | [U2](#u2) Take a turn: speak, and the agent answers out loud | User | Included |
| [Turns](#turns) | [U3](#u3) Interrupt the agent mid-sentence | User | Included |
| [Sessions](#sessions) | [U4](#u4) End a session; what was learned is saved in the background | User, background job | Included |
| [Sessions](#sessions) | [U5](#u5) Resume an earlier session | User | Included |
| [History](#history) | [U6](#u6) Review past sessions | Product's server | Included |
| [User context](#user-context) | [U7](#u7) See, correct and delete what voice-guide knows about me | User | Included |
| [User context](#user-context) | [U8](#u8) Switch to another agent and have it know me | User | Included |
| [Agents](#agents) | [W1](#w1) Create or change an agent | Member | Excluded for now (today: files edited in the repository) |
| [Agents](#agents) | [W3](#w3) Put an agent on a product | Member, through the product's server | Included |
| [User context](#user-context) | [W2](#w2) Grant another workspace access to my context | User | Excluded for now |
| [Timing and load](#timing) | [O1](#o1) See per-stage timing for any turn; run a load test | The team running voice-guide | Included |
| [Turns](#turns) | [F1](#f1) A provider (model or speech) is slow, down or rate-limited | Model or speech provider | Included, as failure modes of [U1](#u1) to [U5](#u5) |

<a id="excluded"></a>
### 3.2 Excluded for now

Where voice-guide grows next. None of it is built or designed yet; the code doesn't implement it, and avoids blocking it where that costs little.

Capabilities:
- Embedding for anonymous visitors: a public key in the product's page, a list of the sites allowed to use it, and limits per key and per address, for a product whose visitors don't log in; such sessions know no user, so no user context ([D32](#d32)).
- Agents registered through the API; until then agent files, published by a script ([D17](#d17)).
- Screen activities: the agent acting on the product's screen (filling a form, opening a page). What the screen shows already reaches the agent as screen input ([D35](#d35)).
- Connectors that fetch a business's knowledge or a user's data from other systems; until then the product sends what it holds ([D35](#d35)).
- More agents, including ones that read other context kinds ([section 7](#agents)).
- Japanese, then other languages: through Speaches with Whisper, or Moonshine's Japanese model ([D21](#d21)); Smart Turn already covers Japanese ([D22](#d22)).
- Logins and accounts; until then one placeholder user ([section 6](#platform-security)).
- A builder for workspaces' members to create agents ([W1](#w1)).
- Sharing context with another workspace ([W2](#w2)).

For production, before anyone outside its maintainers uses voice-guide:
- Model access through the provider's API, billed per token, as the default in place of a development subscription ([D25](#d25)).
- A relay (TURN) server for users whose network blocks direct WebRTC connections ([D19](#d19)).
- Row-level security in PostgreSQL once a second workspace exists ([D12](#d12)).
- A container platform running several instances of each service; any instance serves any turn ([D14](#d14)).

Technical paths, taken when measurements call for them:
- A backend-for-frontend (Rails), when logins, the builder or a second frontend give it a job ([D8](#d8)).
- Turns in a wide-column store (ScyllaDB, or Apache Cassandra on the same protocol) if the PostgreSQL primary can't keep up; without real traffic, the move is an export and import ([D5](#d5)).
- A dedicated vector store, past roughly 5 to 50 million vectors or on measured slowness ([D5](#d5)).
- Kokoro in the browser, for cost at scale, with voice handling echo itself ([D23](#d23)).
- Speech detection in the browser, if interrupting feels sluggish ([D20](#d20)).

<a id="qualities"></a>
## 4. How it works

<a id="context"></a>
### 4.1 System context

A user's browser loads the web app and talks to one Go service: HTTP for agents, for starting and ending sessions and for typed turns, WebRTC for audio ([D11 API transports](#d11)). The Go service holds no conversation between requests ([D14 stateless servers](#d14)); everything it needs is in PostgreSQL. Around it, three services of its own, speech-to-text and text-to-speech in containers (text-to-speech can run natively on a Mac instead, with `KOKORO=mlx`) and the model service on the machine beside voice until it gets one:
- a speech-to-text service, which turns streamed audio into text ([D21 speech-to-text](#d21));
- a text-to-speech service, which turns the reply's text into audio ([D23 text-to-speech](#d23));
- a model service, which reaches the model provider ([D25 model access](#d25)).

Outside voice-guide's own services, it reaches only the model provider and, once a session has ended, the product's server, at the workspace's webhook ([D40](#d40)). Every container runs the same way locally and in the cloud ([D10 containers](#d10)).

<a id="modules"></a>
### 4.2 Modules and boundaries

The Go service is one program with four modules:
- conversation: sessions and their turns: each turn's prompt, the model's reply, what was said, and the time each stage took. It works in text only, whether the words were typed or spoken.
- speech: the audio connection, speech-to-text and text-to-speech, telling when the user has finished, and interrupting. It hands conversation the words it heard, plays the reply, and tells conversation how much of the reply was heard.
- user context: loads the context an agent declares for a session and saves what the session learned. It owns its data.
- agents: agent definitions (persona, activity, voice settings), with their versions.

Conversation and speech together are voice, the part that gives an agent a voice. It decides how a session's audio and text flow, never what an agent says.

The entry point wires the modules together and is the only code that imports all of them. Speech imports conversation, never the reverse; user context imports none of the others; agents imports neither conversation nor speech; conversation reaches user context only through its service. The API's one router, the ticket check and the caller a credential names sit outside the modules and import none of them, so each module's HTTP adapter registers its routes without importing another module. Each speech stage and model access sit behind a swap point, and a vendor's package is imported only by that vendor's adapter ([D2 swap points](#d2)).

Inside each module the shape is hexagonal ([D31](#d31)): the core holds the rules and depends on nothing outside; the HTTP API, WebRTC, speech, the model and the database plug into it as adapters, for example:

```
internal/conversation/
  turn.go, session.go    the core, with its ports
  http/                  inbound adapter
  model/http/            outbound adapters
  store/postgres/
  model/fake/            fakes, one per port, for the core's tests
  store/fake/
internal/speech/
  http/                  inbound adapter
  transport/webrtc/      outbound adapters
  stt/moonshine/
  tts/kokoro/
```

<a id="d1"></a>
> #### D1. One service with four fenced modules
> - Decision: conversation, speech, user context and agents are modules of one Go service, each under `internal/`, with hard boundaries, and the service scales by running more copies of itself. User context keeps its own tables in its own schema, and only user context touches them: conversation calls user context's service directly, the way it calls agents, never its store. Speech is a client of conversation, as the HTTP API is: it hands over the words it heard and says how much of a reply was heard; conversation never imports speech, so it stays text-only and a product without audio uses it unchanged.
> - Needed by: [NFR2 scalability](#nfr2) (no early stage blocks a later one); [NFR8 swappability](#nfr8).
> - Assumes: one team; the modules scale together.
> - Why: one deployable is the simplest to build, run and measure, and running more copies of it covers the scale in view; enforced boundaries keep each module's data and rules in one place (Fowler, "Monolith First").
> - Trade-off: a boundary inside one process can be crossed by mistake, so it needs a check; moving a module into its own service later would be a refactor, not a configuration change.
> - Alternatives rejected: separate services from the start (network hops and operations cost before any measured need); user context behind an interface shaped like a network request, ready to become its own service (machinery for a split nobody plans); a database login per module (separates tables only when modules are heading for separate databases); one module with no boundaries (any code could read any module's tables); conversation and speech as one module (the conversation would depend on audio code, and a product without audio would carry it); speech as its own service now (the heavy speech work already runs in its own containers, and saying how much of a reply was heard would become a network call in the middle of a live exchange; it comes back when audio must run close to users or scale apart from the rest).
> - Status: accepted.
> - Enforced by: Go's `internal/` rule; depguard allow-lists per module in `.golangci.yml`, run by the push hook and CI: conversation may import user context's service but not its store, and speech conversation's service but not its stores; user context imports none of the others; the router, the ticket check and the caller import no module.

<a id="d2"></a>
> #### D2. Every component behind a swap point
> - Decision: each speech stage (transport, noticing speech, speech-to-text, end of turn, text-to-speech), model access and storage sit behind an interface owned by the module that uses it; the implementation is chosen by configuration. A vendor's package is imported only by its adapter. Interfaces carry streams wherever the data is a stream, so implementations can differ in how much input they wait for without changing the pipeline. Storage is always behind a port, since the database is outside the core like any vendor; a vendor or another service gets an interface only where at least two real implementations are planned. Each port has a fake implementation from its first day.
> - Needed by: [NFR8 swappability](#nfr8); [NFR1 latency](#nfr1) (implementations compared by per-stage timing); [NFR2 scalability](#nfr2) (a free fake for load tests).
> - Assumes: each vendor or service swap point has a second real implementation in view (listed with each decision); a store may never get one, and is a port all the same.
> - Why: speech and model vendors change fast in price, speed and terms; swapping one should touch one adapter and one setting.
> - Trade-off: an interface per seam, and adapters to maintain.
> - Alternatives rejected: calling vendors directly from a module's core (every swap becomes a refactor); an interface for everything (indirection with no second implementation); a port only for stores with a second store planned (the core would hold its SQL and import the database driver, and which tables get a port would be argued table by table).
> - Status: accepted.
> - Enforced by: depguard lists, which refuse any unlisted package in a module's core; the fake per port, which the core's tests use, so a port shaped around one vendor fails to fit it; the two-implementations rule is guidance only, checked in review.

<a id="d31"></a>
> #### D31. Hexagonal inside each module
> - Decision: each module's core (its rules and use cases) sits in the module's own folder and depends on nothing outside it. It declares what it needs as small interfaces, its ports. Everything else is an adapter in a subfolder:
>   - inbound adapters (the HTTP API) call the core;
>   - outbound adapters (the WebRTC transport, speech-to-text, text-to-speech, the model, storage, fakes) implement a port.
>
>   The entry point wires the adapters in at startup.
> - Needed by: [D2 swap points](#d2); [NFR8 swappability](#nfr8); [D28 load tests](#d28) (fakes are adapters).
> - Assumes: ports exist only for I/O (storage always; a vendor or service where a second real implementation is planned), which keeps their number small.
> - Why: components can be switched later by changing one adapter and one setting. The swap points are already ports, and the database is one more outside thing behind a port. Go places an interface in the package that uses it, which is where a port lives.
> - Trade-off: more small packages, and a wiring step at startup.
> - Alternatives rejected: layered (handlers, then services, then storage), where the logic depends on storage and vendor code, so a swap reaches into the logic.
> - Status: accepted.
> - Enforced by: depguard lists per folder: a module's core allows the standard library, the modules it may use and, for conversation and speech, `internal/access`, which names the caller; each adapter folder with a list of its own allows its own vendor, and a new adapter folder falls under its core's list until it gets one; any other package under `internal/` allows only the standard library until it gets a list of its own.

<a id="domain"></a>
### 4.3 Domain model

| Term | What it is | Owned by |
|---|---|---|
| Workspace | Owns agents and is responsible for them: an individual or a company | |
| Member | A human in a workspace who builds or manages its agents (excluded for now) | Workspace |
| Agent | The AI that talks: persona, activity, voice settings; versioned | Workspace |
| User | The human who talks to an agent; one account across voice-guide | |
| Session | One conversation between a user and an agent version | Workspace |
| Turn | One exchange in a session: what the user said, what the agent said, and the time of each stage | Workspace (through its session) |
| Context item | Something user context holds about a user (a profile, a session summary, an agent's own record), of a declared kind | Workspace |

Every stored record carries its workspace, and every record about a user carries the user too ([D12 workspace on every record](#d12)). Each table is designed when it is built, and the migrations in `migrations/` are the record of them. So far:
- `workspaces`, and `users`, one per human across workspaces;
- `product_keys`, one row per product key: its workspace, its SHA-256 and first characters, when it was made and when revoked, never the key itself ([D32](#d32));
- `user_references`, a product's own stable ID for each of its users, per workspace, mapped to a user, one per user, both added with the first session the product starts for them ([D32](#d32));
- `webhooks`, one per workspace that has one: the URL on its product's server, the secret deliveries are signed with, kept readable since signing needs it, and when a 410 Gone stopped deliveries ([D40](#d40));
- `sessions`;
- `turns`, one row per turn, numbered from 1 within its session: the user's words, the screen input and the agent's reply as the model sent it, with a status (in flight, answered, lost, cut). A cut turn keeps the whole reply and how many characters of it the user heard ([D36](#d36)). A turn's workspace and user are its session's, by a foreign key. One turn per session is in flight at a time; one still in flight past the turn deadline was cut off, and the next turn saves it lost;
- `turn_stages`, one row per stage of a turn: when it started and how long it took, so that together they say when the stage was reached (speech's stages all start when the user started waiting, so their durations overlap and don't add up), what served it (a provider and model for the model's stages), and what else it reported, such as a reply's token counts ([D13](#d13));
- in the agents module's own schema, `agent.agents` and `agent.agent_versions`, one row per published version ([D17](#d17));
- in user context's, `user_context.context_items`, one row per item, never edited ([D27](#d27));
- `jobs`, the background job queue, owned by the job runner, `internal/jobs`, whose shape a module follows to queue a job, which may name another job it runs after ([D15](#d15)).

<a id="d3"></a>
> #### D3. One user account; records owned by the workspace holding them
> - Decision: a user has one account across voice-guide. Every record about them names the workspace holding it, and by default only that workspace's agents read it. The user can grant another workspace access ([W2 grant another workspace access](#w2), excluded for now). A workspace is one concept whether it serves the public or a company's own users.
> - Needed by: [U8 another agent knows the user](#u8); [W2 grant another workspace access](#w2); [NFR3 privacy](#nfr3); [NFR4 workspace isolation](#nfr4).
> - Assumes: workspaces may be individuals or companies, and a user may talk to agents from several.
> - Why: the same model serves both a workspace open to anyone and a company serving its own users; the user, not the workspace, decides what leaves a workspace.
> - Trade-off: shared context across workspaces needs a grant flow before it works.
> - Alternatives rejected: one account per workspace (the user's profile splits again); context shared across all workspaces by default (no privacy boundary).
> - Status: accepted.
> - Enforced by: the workspace filter on every query but the system's own sweeps ([D12](#d12)); a grant check once grants exist.

<a id="lifecycles"></a>
### 4.4 Lifecycles

A session is started, live while turns happen, and ended, once, when the product ends it or after five minutes idle ([D38](#d38)); an ended session takes no turns, and can be resumed ([U5 resume a session](#u5)). After it ends, a background job saves what it learned ([U4 end a session](#u4)), and then another posts it to the product's server ([D40](#d40)). A turn is listening, then thinking (the model is working), then speaking, then recorded; an interruption ends speaking early and records the reply as far as it was heard ([U3 interrupt the agent](#u3)). The exact states and their transitions are designed when each is built.

<a id="turn-life"></a>
### 4.5 A turn's life

Every stage streams and starts on the first piece of input from the stage before, so the stages overlap ([section 9](#pipeline)).

1. The browser streams the user's audio to voice over WebRTC ([D19 WebRTC](#d19)).
2. voice notices speech ([D20 speech detection](#d20)) and streams it to speech-to-text, which returns rough text while the user is still speaking ([D21](#d21)).
3. The user pauses. Two things happen at once: speech-to-text returns the final text, and the end-of-turn check judges from the audio whether the user has finished ([D22 end of turn](#d22)).
4. Once the check says the user has finished and the final text is in, voice sends the turn to the model; starting the model before the check answers is deferred ([D30 speculative start](#d30)). The stable layers of the prompt and the earlier turns are read from the provider's cache, the turn writes the last saved exchange to it, and the new message, the newest screen input and the words, is read fresh ([D24](#d24), [D25](#d25)).
5. The model's reply arrives word by word. Once the turn is confirmed, voice streams it to text-to-speech, which returns audio in 20 ms packets, starting with a short first phrase, each packet marked with the phrase it speaks ([D23](#d23)).
6. The audio streams to the browser over WebRTC and plays.
7. Off to the side, the turn is saved: the user's words when the turn is confirmed, the agent's reply as far as the user heard it ([D36](#d36)), and the time of each stage ([D13](#d13)).

The user waits only for the pause, the model's first word and the first phrase's audio; the end-of-turn check overlaps the model. The timed stages:
- speech-to-text final text
- end-of-turn decision
- model first word
- first speakable phrase (when the first phrase's text was complete, marked on its packets)
- first audio
- browser playback

A typed turn records reading for the prompt, the model's first text, the model's whole reply and saving; one the user cut in on before the model was asked records only reading for the prompt and saving. One typed on a session's audio connection is spoken too, and speech adds its own stages to the turn's, each counted from the words arriving: the first phrase's text complete and the first audio packet sent, the audio stage naming the text-to-speech that made it. Speech logs them with the model's first text, and saves them with the turn's other stages in the same write. A spoken turn, as built so far, is taken once the end-of-turn check says the user has finished, or the maximum wait is up, and its final text is in, without the speculative start yet ([U2](#u2)); speech counts its stages from the end of the user's speech, when they start waiting: the final text, the check's answer, the turn taken, the first phrase's text complete and the first audio packet sent, which it logs, with the model's first text, and saves with the turn. A stage that didn't happen, such as the audio of a reply cut in on before its first sound or one text-to-speech failed to make, is left out.

<a id="scope"></a>
## 5. Qualities

The non-functional requirements. No target has been agreed: where a target would be a commitment, this section says what the quality needs and leaves the figure undecided until there is evidence ([D13](#d13)). Targets live in the "Qualities" table of the feature they apply to; system-wide ones in [Platform](#platform-qualities).

| ID | Quality | What it means here |
|---|---|---|
| <a id="nfr1"></a>NFR1 | Latency | How long each stage of a turn takes, reported as median and 90th percentile per stage, and the time from the user finishing to the agent's first audio |
| <a id="nfr2"></a>NFR2 | Scalability | Growing from one user to many without work at an earlier stage blocking a later one; each step checked by load tests before it is needed |
| <a id="nfr3"></a>NFR3 | Privacy | An agent reads only the context kinds it declares; what reaches the model and speech providers is known; the repository holds no personal data |
| <a id="nfr4"></a>NFR4 | Workspace isolation | No workspace reads another's records without the user's grant |
| <a id="nfr5"></a>NFR5 | Safe retries | A repeated request (a start pressed twice, a turn resent after a dropped connection) has the effect of one |
| <a id="nfr6"></a>NFR6 | Graceful degradation | When a provider is slow or down, the session carries on in a reduced form (typing, text replies) instead of failing |
| <a id="nfr7"></a>NFR7 | Durability | A user's finished words and every recorded turn survive a server restart; the database, not a server, holds the conversation |
| <a id="nfr8"></a>NFR8 | Swappability | Replacing a speech, model or storage implementation touches one adapter and one setting |
| <a id="nfr9"></a>NFR9 | Embeddability | Another product adds voice-guide to its pages, with its own agents and its own screen, without changes to voice-guide |

<a id="platform"></a>
## 6. Platform

<a id="platform-overview"></a>
### Overview

What every part of voice-guide shares: the technology, the API and data conventions, access, timing, and the rules that keep scale-out possible.

<a id="platform-qualities"></a>
### Qualities

| Quality | Target |
|---|---|
| [NFR1 latency](#nfr1) | Every turn records every stage; no target yet ([D13](#d13)) |
| [NFR2 scalability](#nfr2) | The scale rules below hold in every change; load tests use fakes ([D28 load tests](#d28)) |
| [NFR3 privacy](#nfr3) | An agent reads only the context kinds it declares ([D27](#d27)); the repository holds no personal data, only sample data ([A3](#a3)) |
| [NFR4 workspace isolation](#nfr4) | Every query filters by workspace, but the system's own sweeps ([D12](#d12)) |
| [NFR5 safe retries](#nfr5) | Starting a session and taking a turn each carry an ID, so a repeat has the effect of one ([U1](#u1), [U2](#u2)) |
| [NFR6 graceful degradation](#nfr6) | Every provider failure in [U2](#u2) has a reduced mode: typing, text replies or the browser's own voice |
| [NFR7 durability](#nfr7) | No conversation held only in a server's memory ([D14](#d14)) |
| [NFR8 swappability](#nfr8) | Each swap point has a second implementation in view; swapping one changes one adapter and one setting ([D2](#d2)) |
| [NFR9 embeddability](#nfr9) | voice-guide's own web page uses only the API an outside product would use ([D29](#d29)) |

<a id="platform-assumptions"></a>
### Assumptions

- <a id="a1"></a>A1. Users are on a desktop browser with a microphone; Chrome is the reference browser.
- <a id="a2"></a>A2. English only until Japanese is built.
- <a id="a3"></a>A3. A few workspaces, all the maintainer's, one for each of the maintainer's own products, and one placeholder user until logins; the placeholder user's data is sample data in the repository.
  - The sample lives in `seed/placeholder-user/`. A file of the same name in `seed/local/`, which is not tracked, takes its place for the development product's server and the example product's server, each file on its own, so testing with real data never puts it in the repository.
- <a id="a4"></a>A4. Everything runs on one machine with docker compose, with no cloud spend.

<a id="platform-how"></a>
### How it works

<a id="technology"></a>
#### Technology

| ID | Decision | Needed by | Why | Alternatives rejected | Enforced by |
|---|---|---|---|---|---|
| <a id="d4"></a>D4 | Go for the core service | [D1](#d1); [D19](#d19) | Goroutines suit many long-lived audio connections; WebRTC exists in pure Go (Pion); one static binary per service | TypeScript throughout | `go.mod`; CI |
| <a id="d5"></a>D5 | PostgreSQL 18, the only data store at first: plain tables for the core, JSON columns for per-agent records, pgvector for relevance search | [NFR7](#nfr7); [D3](#d3) | One relational store until a measured need for another; version 18 for native UUIDv7 | A wide-column store for turns from the start (a later path, [section 4.2](#excluded)); a separate vector store | the migrations in `migrations/`; CI starts the database from `compose.yaml`, so local runs and CI share one PostgreSQL version |
| <a id="d6"></a>D6 | sqlc with pgx for queries; goose for migrations | [D5](#d5) | SQL written as SQL, checked and turned into typed Go at build time; plain SQL migrations | an ORM | sqlc checks every query against the migrations, and CI fails when the generated code is stale; depguard keeps pgx in store adapters |
| <a id="d7"></a>D7 | UUIDv7 for every ID | [NFR2](#nfr2) | No central counter, so any instance or store creates IDs; time-ordered, so turns read in order | serial integers | a check on every ID column that the UUID is version 7, with tests; the agents core refuses agent IDs that aren't. A request ID, chosen by the caller only to recognise a repeat, may be any UUID version: browsers' `crypto.randomUUID()` makes version 4 |
| <a id="d8"></a>D8 | React with TypeScript in the browser, calling the Go service directly | [D11](#d11) | The standard web stack; no layer in between until one has a job | a backend-for-frontend now (adds a hop and a service before it has a job; a later path, [section 4.2](#excluded)) | guidance only |
| <a id="d9"></a>D9 | Python for the speech-to-text service; TypeScript for the model service | [D21](#d21); [D25](#d25) | Each is the language its library ships in (Moonshine; the Claude Agent SDK) | running each inside Go (no subscription route to the model from Go; for speech-to-text, [D21](#d21)) | guidance only |
| <a id="d10"></a>D10 | Every part in a container; docker compose locally, the same containers in the cloud; settings from the environment | [NFR2](#nfr2); [A4](#a4) | One way to run everything, so local runs predict cloud runs | tools installed on the machine outside containers | `compose.yaml`, which runs PostgreSQL, Kokoro and the speech-to-text service so far (Kokoro in the `tts` profile and speech-to-text, built here, in the `stt` profile, each started by name, so a bare `docker compose up` doesn't pull Kokoro's 4.5 GB image or build the other); its `app` profile adds voice, its one-time setup, the model service and the test page, built by `Dockerfile`, which `scripts/up.sh` starts; for development, `scripts/run.sh` runs voice, the model service and the page on the machine instead, where `claude-subscription` reaches the Mac's login and `KOKORO=mlx` runs text-to-speech natively |

<a id="swappable"></a>
#### What can be swapped

The technologies named in this document are today's choices. Most sit behind a swap point ([D2](#d2)) in an adapter ([D31](#d31)); a few are foundations, chosen once.

| Part | Today | To swap it |
|---|---|---|
| Speech detection | Silero, in Go through govad | One adapter and one setting ([D20](#d20)) |
| Speech-to-text | Moonshine, in its own service speaking OpenAI's realtime transcription API | Any service with the same API, by setting; or one adapter ([D21](#d21)) |
| End of turn | Smart Turn | One adapter and one setting ([D22](#d22)) |
| Text-to-speech | Kokoro, through Kokoro-FastAPI's streamed Opus | One adapter and one setting ([D23](#d23)); the port takes text and a voice and yields 20 ms Opus packets, so a vendor that gives only raw audio needs an Opus encoder in its adapter |
| Model and provider | Claude through the API, or on a subscription through the Agent SDK; a fake for load tests | One adapter and one setting ([D25](#d25)) |
| Storage | PostgreSQL | One adapter per store, since queries and migrations are written for PostgreSQL ([D5](#d5)) |
| Audio transport | WebRTC with Pion, behind speech's transport port | One adapter, plus changes in the browser, which speaks WebRTC too ([D19](#d19)) |
| Core language and web framework | Go, React | A rewrite; foundations are chosen once ([D4](#d4), [D8](#d8)) |

Every port has a fake implementation used by the core's tests from the port's first day, so each swap point is proven by two working implementations before a second vendor exists ([D2](#d2)).

<a id="platform-api"></a>
#### API conventions

The Go service speaks HTTP for agents, for starting and ending sessions and for typed turns, and WebRTC for audio. Its API is what voice-guide offers: it is designed for outside callers, so a product, or a backend-for-frontend, can sit in front without changing it. Every path starts with `/v1/`, every error is a problem details document ([D33](#d33)), and every request carries a product key, from a product's server, or a credential voice-guide signed, from the user's browser ([D32](#d32)). The exact endpoints are designed with each feature: `GET /v1/agents` lists the agents the caller may start a session with, `GET /v1/voices` lists the voices a session may speak in ([D23](#d23)), `POST /v1/sessions` starts one for the user the product names and answers with the session and its ticket ([U1](#u1)), `POST /v1/sessions/{id}/turns` takes a typed turn in it, the reply streamed back as server-sent events ([U2](#u2)), `POST /v1/sessions/{id}/end` ends it, called by the browser with its ticket or by the product's server with its key ([U4](#u4)), `GET /v1/users/{user}/sessions` lists a user's sessions and `GET /v1/sessions/{id}` reads one with its turns, both for the product's server and its key alone ([U6](#u6)), and `POST /v1/sessions/{id}/audio` sets up its audio connection, which `DELETE` at the connection's own address ends ([D11](#d11)). The one call the other way: once a session has ended, voice-guide posts it, signed, to the workspace's webhook on the product's server ([D40](#d40)). The routes in a session answer a page on any site, the routes a product's server calls none, and voice also serves the drop-in client, the script a product's page loads, at `/client/voice-guide.js`, outside `/v1/` and with no credential ([D41](#d41)).

<a id="d29"></a>
> #### D29. An API for outside products; voice-guide's own web page is its first client
> - Decision: every endpoint and message is designed for a product outside voice-guide to call. voice-guide's own web page uses only that API, with nothing private to it, and so does the drop-in client: a custom element, `<voice-guide-session>`, in the product's own page, never in a frame, which the product's page hands the session ID and ticket its server's start answered with by a call, `join({ session, ticket })`, never an attribute; it takes no product key and starts no session ([D32](#d32)). The page and the element hold a session with the same code, written without React, and each only draws what it shows.
> - Needed by: [NFR9 embeddability](#nfr9); [W3 put an agent on a product](#w3).
> - Assumes: what voice-guide's own page needs is what an embedding product needs.
> - Why: an API used only by its own screen drifts toward that screen; using it the way an outside product would keeps it embeddable. In the product's page, the browser asks for the microphone in the product's own name, and the product restyles the element with custom properties and named parts, while a closed shadow root keeps the product's styles and other scripts out. A call, not an attribute, because session-replay scripts that products run (Hotjar, FullStory, Microsoft Clarity) record the page's attribute values and send them to their own servers, where a ticket would work for anyone reading the recording until its session ends; Retell's and ElevenLabs' browser SDKs take their credential in a call too. Events tell the product's page when the session is over and why.
> - Trade-off: the web page can't take shortcuts; the API is designed before the screen's convenience. The page reaches voice through its development server's proxy, on one origin, so it doesn't exercise the cross-origin requests an embedding product's page makes ([D41](#d41)), which the drop-in client makes from the product's page.
> - Alternatives rejected: a private API now and a public one later (the public one would be a rewrite, and the private one would shape every decision until then); the drop-in client as a frame served by voice (the microphone prompt names voice's site, which the product must also allow on the frame, and the product can't restyle it); the ticket as an attribute (recorders copy it), or one the element reads and removes at once (recorders see it set before it goes); a React component (every product would load React).
> - Status: accepted.
> - Enforced by: guidance only, checked in review: the web page calls only the public API, with the ticket the development server, playing the product's server, hands it with each session ([D32](#d32)); a published API description for products to build against comes with embedding ([W3](#w3)). The drop-in client's tests: its built script holds no React, no product route and no product key, takes only custom properties named for it, and defines the element once however often it is loaded; the element removes a `ticket` attribute at once, with a warning naming `join`, never using it, its shadow root is closed, and no event it fires carries the ticket. The page holds its session only through the element's session code, `JoinedSession`, checked in review, and its test, `web/src/App.test.ts`, renders what that code shows: the agents to pick, the transcript, and beneath each reply what answered and how long the user waited.

<a id="d11"></a>
> #### D11. HTTP for agents and sessions, WebRTC for audio
> - Decision: the browser calls the Go service over HTTP for agents, for starting and ending sessions and for typed turns, whose reply streams back on the same request as server-sent events. It opens a session's WebRTC audio connection with one request, in the shape WHIP (RFC 9725) gives it:
>   - `POST /v1/sessions/{id}/audio` takes the browser's SDP offer (`application/sdp`) and answers 201 with the SDP answer and `Location: /v1/sessions/{id}/audio/{connection}`, the connection's own address, its ID a UUIDv7;
>   - `DELETE` at that address ends that connection and no other, with 200, and answers 404 once it has ended or been replaced; a reply still reaching the user is cut where they stopped hearing it, as an interrupt is, since the caller chose to stop listening ([D36](#d36));
>   - the answer waits for the server's own addresses, so no addresses trickle in later (no `PATCH`, no ETag);
>   - where WHIP and the API's conventions differ, the API's win: one endpoint per session, errors as problem details, a `GET` answered 405 rather than an empty 200, and the CORS answers of every route in a session ([D41](#d41));
>   - the server's last message on the connection's data channel says why it ends it: `{"type":"closing","reason":"replaced"}`, or `"stopping"`, or `"ended"` (the caller ended this connection, or its session has ended, [D38](#d38)), or `"expired"` (the ticket that opened it passed its four hours, so the page doesn't reconnect, [D32](#d32)).
>
>   While a session's audio connection is open, its typed turns go over that connection's data channel instead of HTTP, so the server holding the audio takes the turn and plays its reply, and the turns the user speaks into its microphone are taken there too. Each message is a JSON object whose `type` says what it is:
>   - the browser sends `turn`, with `request_id`, `words` and an optional `screen_input`, or with `opening` set and no words for the agent's opening ([D18](#d18)), as over HTTP; an opening sent while another turn is taken is refused at once, never interrupting it;
>   - the browser sends `screen` when what the product's screen shows changes, with `screen_input`, the blocks that changed, and `n`, which counts the `screen` messages sent on the connection. Speech holds the newest block of each name for the next turn taken on the connection, spoken, typed or the opening, a block that turn sends itself winning, and lets a block go only once a turn carrying it is saved: a turn refused, or taken back as the user talks on, leaves it for the next. How the turn ended, `done` or `lost`, carries `screen_taken`, the `n` of the last `screen` message kept before it was taken: only then is a spoken turn past being taken back. A `screen` message that can't be used, alone or with the blocks held, those a turn is carrying included, is answered `screen_refused` with its `n` and a `detail`, and nothing of it is held. The page sends a change only while little waits in the channel's buffer, keeping the newest of each name until then, so changes made many times a second never pile up ahead of a turn. What is held lives only with the connection, so the page sends every change no turn has taken again on its next ([D35](#d35));
>   - the server sends `hearing` while the user speaks a turn, with a `request_id` it makes for the turn and `text`, the words heard so far (each utterance's final text, or its rough text until then), each time they change; then once more with `final` set and the words the turn is taken with, before any other message about it, or with no text when nothing was heard, so the page lets go of what it showed, or with `failed` also set when speech-to-text couldn't hear all of it and no turn was taken, with the words heard so far, which the page gives back for the user to send or say again. The spoken turn is then taken as a typed one is, and its other messages follow under the same `request_id`. `hearing` messages can come between another turn's messages, since the user may speak while the agent answers; speech that began while the agent's reply reached the user is told once it is held, or at its pause when the reply stopped reaching the user during it, since short speech over the agent is passed over and never told ([D37](#d37));
>   - the server sends `text` for each piece of the reply as it arrives, then how the turn ended: `done` with the turn's `turn_id`, `seq` and `model` (what answered it, as provider/model, or only the model voice asked for when the reply ended without a report it could read, as a lost reply, one cut short and an unreadable report do; left out when the user cut in before the model was asked, as the `done` event over HTTP carries too, so a page can show it and a fake model's fixed reply isn't taken for a broken agent), `lost` once a reply that had started didn't finish (with them, once the turn was saved), `refused` with a `reason` and a `detail` that says why in words, or `failed` when something broke before the reply, when it may have been taken, so the page sends it again with the same request ID;
>   - the reasons for `refused`: `busy` (two turns already wait behind the one being answered, or the session is answering a turn sent another way, such as over HTTP; this turn wasn't taken, so the page puts the words back and sends them again as a new request, keeping the request ID only when this send was itself a retry), `repeated` (the request ID already took a turn: count it as taken; its reply is neither sent nor spoken again), `unusable` (the words or screen input can't be used), `unreadable` (not a turn), `no-session`, `ended` (the session has ended; the server then ends the connection, [D38](#d38)), `opening-refused` (the session has a turn, or its agent waits for the user: the agent doesn't open, and the page lets it go);
>   - after `done` or `lost` comes `spoken`, once the reply's audio has all played, with `failed` set when text-to-speech failed part way, so the page says why it went quiet; or, when the user cut in, `interrupted`, with `heard`, the reply's text as the agent remembers it: as far as the user heard it, empty when nothing was, or whole when the cut couldn't be saved. For a turn the user spoke, either carries `first_audio_ms`, how long after their speech ended the reply's first audio went out, once some did;
>   - every message about a turn carries its `request_id`. The connection was opened with the session's ticket, so its turns are the session's user's, typed or spoken, and carry none of their own;
>   - a turn sent while the agent answers or speaks interrupts it, and so does speech held over it, before its turn is taken ([D37](#d37)): the reply's audio and text-to-speech stop at once, the model's reply stops if it is still coming (or isn't asked for, when the turn hadn't reached the model), and the reply is saved as far as the user heard it before the new turn is taken. Turns are taken in the order sent, each once the one before is saved, every message about a turn comes before any about the next, and at most two wait; the same request ID sent again while its turn is answered is a retry, which waits without interrupting it, one at a time, and which a later turn interrupts along with the turn it retries;
>   - a reply that plays to its end is saved whole, as sent. The one gap: when the connection drops mid-reply, the last pieces may be saved without reaching the page, and the turn is saved lost; a reply whose connection drops part way is still saved whole, not as far as it was heard. One the caller ends with `DELETE` is cut where it was heard.
> - Needed by: [U1](#u1); [U2](#u2); [U3](#u3).
> - Assumes: one browser client for now.
> - Why: request and reply suit agents and sessions; a typed reply streams the way the model providers' own APIs stream theirs, read with `fetch`, since the browser's `EventSource` can't send the ticket header and a ticket in the URL would land in logs; live audio needs WebRTC ([D19](#d19)). WHIP's shape is an IETF standard's, so its clients and documentation apply. Each connection has its own address so a closing tab's late `DELETE` can't end the connection of the tab that replaced it. No trickle, because gathering only the server's own addresses takes no network round trip and one request is the simplest for any product; trickling comes back with a relay server, whose addresses are slow to gather. The closing reason lets a page tell a replacement, where it stays closed, from a stop, where it reconnects. Typed turns on the data channel reach the server that plays the reply, with no message passed between servers and no routing of a session's requests to one server. A turn while the agent is still speaking interrupts it rather than waiting or playing over it, since a user who types while the agent talks means to cut in; one message does it, so any product gets interrupting with no extra message. Two waiting is more than a person typing makes, since saving a cut takes milliseconds, so more means a script or a stuck key, and each turn taken costs a model call. A dropped connection isn't treated as cutting in, since nothing says how much reached the user before it dropped; one the caller ends is, since the page stopped the sound at that moment. Reasons instead of HTTP statuses, since the data channel isn't HTTP, and `spoken` because the page can't tell from the text when the audio ends.
> - Trade-off: two transports to handle.
> - Alternatives rejected: everything over one WebSocket (no reliable echo cancellation, [D19](#d19)); typed turns over a WebSocket of their own (a third transport, for text alone); a WebSocket to set up the audio connection (a second long-lived connection that must reach the same server, for messages the connection's own data channel can carry).
> - Status: accepted.
> - Enforced by: the HTTP API is two adapters, `internal/conversation/http` for agents, sessions and typed turns and `internal/speech/http` for audio connections, each registering its routes on the API's one router, `internal/api`, with tests that each answers as specified; WebRTC is speech's transport, `internal/speech/transport/webrtc`; speech's core takes turns on the data channel, with tests that a turn's text arrives at once and its audio in real time, that a turn while the agent answers or speaks stops its audio at once and saves it as far as it was heard before the next turn's prompt, that turns sent one after another are taken in order with no more than two waiting, that a retry doesn't interrupt its own turn and is the turn the next one interrupts, that only one retry waits, that the user can interrupt again and again, that a piece arriving as the user cut in is neither shown nor said, that each refusal carries its reason, that text-to-speech failing leaves the text flowing, and that a turn in flight is saved lost when its connection ends; tests that a spoken turn's words reach the page as `hearing`, the last final, before its reply under the same request ID, with `first_audio_ms` in `spoken`, that one speech-to-text couldn't hear in full reaches it as `failed` with the words heard, and that speech over the agent reaches it only once held ([D37](#d37)); which transport carries what is checked in review.

<a id="d33"></a>
> #### D33. Errors as problem details; the version in the path
> - Decision: every error reply is an RFC 9457 problem details document (`application/problem+json` with type, title, status and an optional detail). A problem gets a type of its own when a client must tell it apart from another error with the same status, as with the four refusals of a session start with 422: session context that doesn't match the agent, a request ID reused for a different request, a voice or speed the service can't take, and a voice the text-to-speech service doesn't have; with the 409 for a turn or an audio offer in a session that has ended, told from another turn in flight, which may be sent again, and the 409 for an opening the session doesn't take, which a client lets go; and with the 401 for a ticket past its lifetime, told from a credential that is wrong, so a page says the session has expired ([D32](#d32)). Its type is a reference relative to the API with the full path, `/v1/problems/<name>`, the name in lower-case words joined by hyphens, and its title sums up the type; every other problem's type is `about:blank`, titled with the status. Every path starts with `/v1/`; the number changes only for a breaking change, and additions don't need one.
> - Needed by: [D29](#d29); [NFR9 embeddability](#nfr9).
> - Assumes: products' clients act on the status code first, then on the type where two errors share a status, and show the detail to people.
> - Why: problem details are the IETF's standard error format for HTTP APIs, so products' tools already read them; a relative type under the API's own path needs no domain voice-guide doesn't have, and clients compare it as sent; a version in the path is the most common choice and the easiest to see in logs, route and test.
> - Trade-off: a breaking change means serving two versions side by side for a while.
> - Alternatives rejected: an error shape of voice-guide's own (every product learns it); an error code field beside the standard ones (a client would read two fields to know one error); absolute web addresses as types (no host is fixed, and each deployment has its own); a `tag:` URI, which RFC 9457 allows and which doesn't depend on the host or where the API is mounted (it needs a naming authority, a domain or address voice-guide controls, which it doesn't have yet); the version in a header or media type (harder to see, route and test).
> - Status: accepted.
> - Enforced by: tests that errors, including unknown paths and wrong methods, answer as problem details, and that each problem with a type of its own carries it.

<a id="platform-data"></a>
#### Data conventions

<a id="d12"></a>
> #### D12. Workspace on every record, user on every record about one; every query filters by workspace but the system's sweeps and the key check
> - Decision: every stored record carries its workspace ID from the first table, every record about a user also carries the user's ID, and every query filters by workspace, except the system's own sweeps, ending idle sessions ([D38](#d38)), and claiming background jobs and deleting old ones ([D15](#d15)): each finds its rows by one condition across workspaces, the same for every workspace; ending and deleting read nothing out, and the claim reads out only the job it takes, which then works within its own workspace, the workspace staying on every record each touches. The product key check is the other exception: it finds a key by its hash across workspaces, since the key is what names the workspace, and reads out only that workspace ([D32](#d32)). Records that aren't about a user, such as an agent's versions, carry only the workspace; workspaces and users themselves carry neither, since one user's account spans workspaces ([D3](#d3)). Row-level security in PostgreSQL is added when a second workspace is created; the sweeps then run under a role allowed across workspaces.
> - Needed by: [NFR4 workspace isolation](#nfr4); [D3](#d3).
> - Assumes: a few workspaces, all the maintainer's, until logins.
> - Why: adding a tenant key after launch means rewriting data access; filtering in every query is enough while every workspace is the maintainer's own, since every query has the filter and the PostgreSQL tests check it.
> - Trade-off: until row-level security, a query that forgets the filter is caught only in review.
> - Alternatives rejected: row-level security from the start (setup cost with nothing yet to isolate).
> - Status: accepted; row-level security deferred until a workspace's product is run by someone other than the maintainer.
> - Enforced by: every query names the workspace but the sweeps and the key check, whose queries say why they don't; each store adapter has a test, against PostgreSQL, that another workspace's rows are out of reach, and each sweep a test that it acts on every workspace alike.

User context's tables live in their own schema ([D1](#d1)), and so do the agents module's (`agent`). Turns sit behind their own storage interface, read by session in time order, so they can move to another store if PostgreSQL stops keeping up ([section 4.2](#excluded)).

<a id="platform-security"></a>
#### Security and access

Until logins, voice-guide knows a few workspaces, the maintainer's own, one for each product, and one placeholder user, the reference in `seed/` that a development product's server names ([A3](#a3)), and the users products name by their own references. Every request names its workspace, by a product's key or a ticket voice-guide signed, and every session and turn is checked against its workspace and user ([D32](#d32)), so logins add who the user is without changing what is checked.

Audio connections ([D19](#d19)), by actor:
- The user's browser gets a connection's ICE password and the server's certificate fingerprint only in the answer to its ticket-checked request.
- Someone else on the same network can send packets to a private address's audio port, but can't join a connection without that password, nor hear it without the browser's own DTLS key, whose fingerprint was in the offer.
- Someone on the internet can't reach the port at all on a laptop: public addresses take audio only when named. An announced address changes only what the answer says, not where audio is taken; the containers publish their one audio port on the host's loopback only, so nothing else on the network reaches it.
- Someone with a valid ticket could open many connections that never connect, each held about 30s until ICE gives up. The per-server limit caps them: measured on an M3 Pro, one such connection costs about 220 KB and 23 goroutines, and its listener one goroutine more, about 5 KB, so 100 cost about 23 MB, and the ticket names who did it. Only a connection whose microphone sends audio holds the listener's 8 s of audio and speech detection's state, about 266 KB more.
- Someone with a valid ticket could stamp each microphone packet far ahead in time, so each counts as up to 10 s of missing audio, 312 frames of 32 ms. Whole frames of missing audio count as silence without running speech detection, which would take about 160 ms of CPU for each such packet; skipped, they take about 6 microseconds (measured on an M3 Pro), so a connection can't take a core this way.

<a id="d32"></a>
> #### D32. A product's server starts each session with its key; the user's browser holds only what voice-guide signed
> - Decision:
>   - A product's server proves which workspace it acts for with its product key, sent on the routes a product's server calls: listing agents and voices, starting a session, ending one and reading past sessions. voice-guide keeps only each key's SHA-256 and first characters; a key is shown once, when made; several can work at once, so a new one replaces an old one without a gap; revoking one stops it at once.
>   - The product's server, not the user's browser, starts a session: it sends its key, its reference for the user, the agent and the session context ([D35](#d35)). A user reference is the product's own stable ID for the user, not a name or an email address, which change; it names the same voice-guide user every time, added with the first session the product starts for them; the product never sees voice-guide's user IDs.
>   - The start answers with a ticket, which the product's server hands to the user's browser: a credential voice-guide signs (HS256) naming the session's workspace, its user and the session itself, which names the agent, which works for its turns, its audio connection and its end, and stops when the session ends, or four hours after the session started, whichever comes first; a repeat of the start, sent when its reply was lost, gets the session back with a fresh one, which expires when the first does, so no repeat stretches a session's tickets; past the four hours it gets the session without one. Past four hours the product starts a new session. voice-guide checks a signed credential's signature, algorithm and expiry without reading the database, with five seconds' leeway for clocks apart.
>   - A ticket sent for another session's path is answered 404, as a session that doesn't exist; on a product's route, 401; past its four hours, 401 of type `/v1/problems/ticket-expired`, so the page tells the user the session has expired and to start a new one. The session's end still takes a ticket voice-guide signed for 15 minutes past its four hours, so the page, learning its ticket ran out, or a user who stayed on ends the session at once and its summary runs, rather than when the idle sweep finds it. An audio connection a ticket opened ends at the ticket's four hours too, a reply still playing cut where the user stopped hearing it and no turn taken from then on, typed or spoken, even while the connection takes its time to close, telling the page the reason `expired`; speech refuses an audio offer whose ticket has expired by its own clock, which the leeway for clocks apart can let past the credential check, with the same 401.
>   - The product's server can end any session of its workspace with its key (`POST /v1/sessions/{id}/end`), its way to stop a session whose ticket was stolen; another workspace's session is answered 404. Its only other route naming a session is the read of one ([U6](#u6)), which takes a key and never a ticket, and so is not one of a session's ticket routes ([D41](#d41)); the turns and the audio connection take no key, and no route takes one sent from a page, with an `Origin` header ([D41](#d41)).
>   - Rotating voice-guide's signing key, read from the environment, means restarting every instance: every audio connection drops, every ticket stops working, and a browser reconnects only with a ticket from a new start. While instances restart, those not yet restarted still take the old tickets and the others refuse them.
> - Needed by: [NFR4 workspace isolation](#nfr4); [NFR9 embeddability](#nfr9); [D12](#d12); [D35](#d35); [U1](#u1) failure mode 1; [U2](#u2) failure mode 11.
> - Assumes: a product's server can keep a secret; clocks agree to within seconds.
> - Why:
>   - The product embeds voice-guide, answers for its users and pays for their sessions. Every start passes its own server, so it decides who may start a session, how often, and what the agent is told about the user; its key never reaches a browser.
>   - The user talks to an agent on the product's page. Their browser holds a ticket for their own session, and can't change what the product says about them, since it never sends it.
>   - An attacker wants to read another user's context or spend the product's model budget. Without the key they can't start a session or name another user; a ticket they steal works only in its one session, never to start another. A copy of voice-guide's database doesn't let them act as a product: it holds only hashes, and a key's 256 random bits can't be guessed back from its hash, so a fast hash protects it as well as a slow one, which only helps secrets people choose.
>   - Any instance serves any request ([D14](#d14)): a ticket is checked by its signature, and a key by one indexed read, made only on the routes that take a key.
>   - Four hours is a round guess, to be measured once products use tickets: a practice session runs well under an hour, so the cap never cuts a real one short, while a stolen ticket still stops on its own even in a session kept alive; counted from the session's start, so asking again can't stretch it. Fifteen minutes past it for ending is a round guess too: long enough for End pressed late, while the idle sweep ends a session nobody touches within five minutes, and short enough that an old ticket stays dead. A ticket for another session looks like one for a session that doesn't exist, so it learns nothing about other sessions.
>   - Voice agent platforms work the same way: the product's server asks for a short-lived credential with its own key (Retell, the OpenAI Realtime API), and Retell's server creates the call with the agent and what it should know about the user.
> - Trade-off: the product needs a server, and calls voice-guide once before each session; a ticket can't be withdrawn on its own: a stolen one works until its session ends, which the product's server can do at once with its key, or for four hours; a session the user keeps going past four hours ends for them, and the product starts a new one; rotating the signing key drops every user's audio and ticket at once; a revoked key stops new sessions, not those already started, and a start whose key was checked before the revoke committed still finishes, within its deadline. A key check the database can't answer is answered 503, so the product retries rather than replacing a key that works.
> - Alternatives rejected:
>   - The product's server signing its own tokens with a shared secret: no call to voice-guide, but voice-guide must keep every product's secret readable to check signatures, so one leak of its database lets an attacker act as any user of any product; and the product, not voice-guide, decides how long a token lives.
>   - A public key in the product's page with a list of allowed sites: no server needed, but the key is read from the page's source and the site check is faked outside a browser, so anyone can run up the product's bill; and every visitor is anonymous, with no user context and no memory of past sessions. Kept for products whose visitors are anonymous ([section 4.2](#excluded)).
>   - The browser starting the session with a token from the product, refreshed every minute: the user could edit what the agent is told about them, and start sessions in a loop on the product's bill without its server seeing them.
>   - Single-use tokens: a script injected into the product's page can ask the product's server for another, so they stop little.
>   - The product's key in the browser (anyone could read it and act as any user); long-lived sessions with cookies (logins and a session store before either has a job); no check until logins (later endpoints would copy the gap, and isolation couldn't be tested).
> - Status: accepted. The product's server starts each session with its key and hands the browser a ticket, which every session route takes; the web page, the scripts and the checks all work this way.
> - Enforced by: routes registered only through the API's one router, `internal/api`, whose `HandleTicket`, for the routes the browser calls, `HandleSessionEnd`, for the session's end, and `HandleProduct`, for those a product's server calls, are the only ways to register one and always add the check; tests that every route refuses a request with no credential, a ticket issued in the future, edited after signing, unsigned or signed with another key or algorithm, one without a workspace or user or naming the nil UUID as either, and a credential naming no session; that a product key is refused unread on the routes the browser calls but the session's end, refused when revoked, unknown or malformed, answered 503 when it can't be checked, and never logged or printed, being held in a type that shows itself redacted; that only a key's hash is kept; that the router marks a key's caller as a product, so its end reaches any session of its workspace; that a start answers with a ticket for its session, a fresh one on a repeat, in a reply no cache keeps; that a ticket reaches its own session's routes, on any instance, with no database read, and is answered 404 on another session's, on a path whose session ID isn't a UUID, and in another workspace, 401 on a product's route and once the signing key has changed, and 401 of type `/v1/problems/ticket-expired` past four hours and the leeway, and not before, but on the session's end, which takes it up to 15 minutes past four hours, not a second more, and never one signed with another key; that a ticket minted by a repeat of the start expires with the session's first, one minted with the start recorded a second ahead still lives four hours, and a repeat past the four hours gets none; that an audio connection ends with the reason `expired` when the ticket that opened it does, on a clock the test sets, a reply playing then saved cut where it was heard, and no turn taken then, one waiting or one read as the cap passed, typed or spoken, while the connection takes its time to close, nor one read once a connection has ended, and that an offer with a ticket expired by speech's clock is answered 401 of type `/v1/problems/ticket-expired`; that a ticket naming no session, or meant to live over four hours, is refused, and one carrying a claim the check doesn't read still works; that a session ended by its end or the idle sweep refuses its ticket's turns and audio, while ending it again answers as before; that the same ticket reconnects the audio; that revoking the key leaves started sessions' tickets working; that the product's key ends any session of its workspace and another workspace's is answered 404; that a ticket never prints, being held in a type that shows itself redacted; the page's test that audio ended as `expired` is the session over, never reconnected; and a test in each module's HTTP adapter that every route it registers refuses a request with no credential, and every route in a session but its end a product key.

<a id="d41"></a>
> #### D41. A session's routes and the drop-in client answer pages on any site; a product's routes answer none
> - Decision:
>   - The routes in a session, which the user's browser calls with its ticket (its turns, its end, its audio connection and hanging it up), answer a page on any origin: `Access-Control-Allow-Origin: *` on every answer, refusals included, so the page reads why it was refused, with `Location` and `Retry-After` readable, and no credentials mode. The router answers their preflight itself, from the route alone: 204 with the route's method, `Authorization` and `Content-Type` as the headers a page may send, whatever it asked for, and two hours to keep the answer; no handler runs and no credential is read.
>   - The routes a product's server calls, listing agents and voices, starting a session and reading past sessions, send no CORS headers, and their preflight is refused with 405, as any other `OPTIONS` request is.
>   - A product key sent with an `Origin` header is refused with 401, before it is read, on every route that takes one, the session's end included: a browser names the page's origin on every request a page on another site makes, and a server's HTTP client names none.
>   - voice serves the drop-in client at `/client/voice-guide.js`, with no credential, to any origin, from the folder `VOICE_CLIENT_DIR` names (`web/dist-client`, where its build writes it, unless set; a folder named that doesn't exist stops the start): that one file, never one a request's path names nor one a link leads to outside the folder, looked at for each request and read again only once it is another file or its size or time has changed, so a new build is served without a restart; 404 until it is built. Anything but a plain file in its place is refused without being opened, since opening a pipe would wait for a writer. Every load asks whether it changed (`Cache-Control: no-cache`), answered 304 when it hasn't, by a tag made from its content.
> - Needed by: [W3](#w3); [D29](#d29).
> - Assumes: no route reads a cookie, or anything else a browser sends on its own.
> - Why:
>   - CORS stops a page on one site from using what the browser holds for another, its cookies, and from reading the answers. voice reads no cookie: a session's route takes only the ticket in the `Authorization` header, which a page sends only if it holds it, and whoever holds it can send it without a browser. Answering any origin gives a page nothing its ticket doesn't, so a product embeds an agent on any of its sites without registering them.
>   - An attacker's page open in the user's browser can't read the ticket from the product's page, which is another origin, and can't get one of its own: a start takes the product's key, which never reaches a browser ([D32](#d32)).
>   - A product's routes take its key, which belongs on its server; with their preflight refused, a browser never sends the key there, so a product that puts its key in its page finds its start failing at once. The session's end takes the key too ([D32](#d32)) and is open to any origin, so without the `Origin` check a product could end sessions from its page with its key, which every visitor could then copy to start sessions for any of the product's users, and any page could have its visitors' browsers send guessed keys to the end, each costing a read, from many addresses. Refused unread when a page sends it, a key works only from a server.
>   - A browser sends no credential with a preflight, so it is answered from the route alone; the same headers whatever was asked keep an odd or huge request from shaping the answer. Two hours is a round guess past a session's length: Chrome, the reference browser, keeps a preflight that long and Firefox up to a day, so there a session's routes are asked about once each, browsers keeping one per address; Safari keeps one about 10 minutes, so there a long session's later turns pay a preflight again. A change to what the routes take reaches browsers within two hours.
>   - A module script loads in CORS mode, so the script needs the header to run on another site. Asking at every load lets a new build reach every page at its next load, for a round trip on the page's load, never on a turn, and a build kept in memory while its file is unchanged spares each load a read; the tag comes from the content, never a time, so every instance serving one build gives the same tag, and during a deploy each load gets one whole build, which works with either, both speaking `/v1/`.
> - Trade-off: anyone may load the script and send requests from any page, each still needing its ticket; a session's first typed turn, audio offer and end each wait for a preflight, in Safari again every 10 minutes; a product can't limit which sites show its agents, and can't end a session from its page.
> - Alternatives rejected:
>   - An allowlist of each product's origins: it guards nothing the ticket doesn't, and each product would register its sites, by script, before embedding an agent; deferred until products have dashboards and ask to limit where their agents appear.
>   - Echoing the origin with credentials allowed: no cookie is wanted, and allowing them would invite one.
>   - No CORS, the product's server passing every request on: a hop added to every turn, and every product running a proxy for streamed replies and audio offers.
>   - The script served from somewhere else, a CDN or the product's own site: served by voice, the script and the API come from one build with nothing more to run; a CDN in front comes with production.
>   - Keeping the script for minutes: saves a round trip only on loads within that time, while a new build, or one rebuilt in development, would reach pages late.
> - Status: accepted.
> - Enforced by: the router's tests: each session route answers a preflight from any origin with its method, the two headers and two hours, whatever headers were asked for, without running its handler or reading a key, even one sent with it; a request from another site is refused 401 without its ticket, readably, and reaches its handler with it; a product's routes refuse their preflight and send no CORS header; a product key sent with an origin is refused unread on every route that takes one, while the product's server, sending none, ends a session with its key; an `OPTIONS` request with no origin or method, or a method the route doesn't take, is refused, and another method carrying a preflight's headers is handled as itself; the script is served as JavaScript in UTF-8 to any origin, with its content's tag, 304 while unchanged; no other file is served, by any path, nor through a link out of its folder, nor a folder in its place, and a pipe in its place is refused at once; a file unchanged in identity, size and time is served from what was read, and read anew once its time moves on; 404 until it is built; one build gives one tag on two instances; a new build is served at once with a new tag; and voice's test that it refuses to start when `VOICE_CLIENT_DIR` names no folder.

<a id="platform-observability"></a>
#### Timing

<a id="d13"></a>
> #### D13. Every turn timed by stage
> - Decision: every turn records the time of each stage ([section 4.5](#turn-life)) and which implementation served it, stored with the session. Reports give median and 90th percentile per stage. The slowest stage is worked on first; each change is measured before and after and kept only if it helps. No latency target until there is evidence of where users find it slow or are cut off. What the user feels is measured in the page as well: from pressing send to the reply's first text, and to its first sound, the first moment the agent's audio track rises above -40 dB below full scale (under the quietest speech, above the near-silence between words), looked for every 10 ms. The gap between that and voice's first audio packet sent is the network, the browser's jitter buffer (about 20 ms on one machine) and the 50 ms of silence each phrase starts with. For a reply the user interrupts, the page also times how long after the press its sound stopped: the end of its last sound before 500 ms of silence, longer than the pauses inside a reply and shorter than the wait before the next reply can sound. That time holds the new turn's trip to voice, voice stopping the reply, the last packet's trip back, the browser's jitter buffer and the 11 ms window the level is read over. The page mutes its own playback at the press, so it reads the level from the received track; the user hears silence at once, and the point voice records as heard can include up to a trip plus a jitter buffer of audio the page had already muted. For a turn the user spoke, the page can't see when they finished speaking, since voice judges it from the audio, so voice tells the page, with the turn's last message, how long after the speech ended the reply's first audio went out, and the page shows it as finished speaking to first sound: short, by a trip and a jitter buffer, of what the user hears.
> - Needed by: [NFR1 latency](#nfr1); [O1](#o1); [D2](#d2) (implementations compared).
> - Assumes: measurements on one machine say which stage is slowest, if not the absolute times at scale.
> - Why: "faster" means nothing without a split by stage; a target set before measuring is a guess.
> - Trade-off: timing code on every turn; no number to hold a change against until a target is set.
> - Alternatives rejected: one end-to-end number (hides which stage to work on); a target now (no evidence).
> - Status: accepted.
> - Enforced by: tests that a typed turn records its stages (reading for the prompt, the model's first text, the model's reply, saving, or only the first and last when the user cut in before the model was asked, the save then holding the words' save too) with their times, exact on a clock the test steps for a reply answered, lost or cut, and what served each, the model's stages naming the provider and model that answered, or only the model voice asked for, with no report, when the reply ended without one it could read: lost, cut short, or a report that didn't read; the times are written once the caller has the whole reply, and tests that a slow or failed write delays and fails nothing the caller sees. The page's tests check that a reply's first sound and, for an interrupted reply, its stop are timed from the press, past the gaps between words, and credited to the reply that was sounding, also when a later send is refused or a third comes before the stop, and that a spoken turn's words show as they are heard, then its reply with finished speaking to first sound, and that a spoken turn taken before a typed reply's first sound has that sound. Speech logs its stages for each turn typed on an audio connection, with a test that each stage is logged in order from the words arriving, and for each turn the user spoke, with tests that each is counted from the end of the speech, which the audio places. It also saves them with the turn's other stages, with tests that a spoken turn saves its five, from the end of the speech and with the text-to-speech named on the audio stage, a typed turn only its two, a reply cut in on before its first sound or one text-to-speech failed to make none of the audio's, and, against the database, that its stage list holds them.

<a id="platform-scale"></a>
#### Scale and resilience

The rules that keep growth open:
1. No server keeps a conversation in memory between requests ([D14](#d14)).
2. Workspace ID on every stored record, and user ID on every record about a user ([D12](#d12)).
3. IDs need no central counter ([D7](#d7)).
4. Slow work runs as a background job, never inside a request ([D15](#d15)).
5. An audio connection can reconnect to any instance ([D14](#d14)), and each instance holds a bounded number of them ([D19](#d19)).
6. The same containers run locally and in the cloud; settings come from the environment ([D10](#d10)).
7. Database changes stay backward compatible once anything is released.
8. Scale is simulated locally with fakes for the model and speech ([D28](#d28)).

<a id="d14"></a>
> #### D14. No conversation held in a server's memory
> - Decision: any instance can serve any turn. The conversation's source of truth is the database; a model process kept open per session for speed is a disposable cache that any instance can rebuild. An audio connection that drops reconnects to any instance.
> - Needed by: [NFR2 scalability](#nfr2); [NFR7 durability](#nfr7).
> - Assumes: rebuilding a cache costs one slower turn, not a failed one.
> - Why: state held on one machine can't spread across servers and is lost when that machine restarts.
> - Trade-off: each turn reads the conversation from the database or a cache, instead of trusting memory.
> - Alternatives rejected: a model process held per session as the source of truth (pins a user to one machine).
> - Status: accepted.
> - Enforced by: a test that a turn served by a fresh instance continues the conversation.

<a id="d15"></a>
> #### D15. Slow work as background jobs, in one PostgreSQL queue for every kind
> - Decision: work that takes longer than a turn (saving what a session learned, later grading or compacting a long conversation) runs as a background job, never inside a request or a turn. Jobs wait in one table, `jobs`, for every kind:
>   - its columns are only the queue's own: the job's ID, workspace, the user it is about if any, its kind, when it may next run, how many runs it has had, its state (waiting, done or failed) and why its last run failed;
>   - each kind's inputs are one `args` JSON value that only that kind's handler reads, decoding it into a Go type of its own, so a new kind adds no column and no migration;
>   - the data a job works on stays typed in its module's own tables, with their own constraints; the job only points at it.
>
>   The job runner, `internal/jobs`, owns the table and the shape of a row: a module queues a job by inserting its row, its kind and its args in the shape that kind's handler reads, in the same statement or transaction as the write that calls for it, so the job exists exactly when that write commits; nothing else outside the runner writes to it. A job queued in the same write as another may name it to run after: it is claimed only once that one is done or has failed for good, however often that one is tried again, with no ceiling on the wait, since a run let go as busy doesn't count, as a session's delivery waits for its summary ([D40](#d40)). Each instance runs one runner per group of kinds that wait on the same thing, each claiming only the kinds it has handlers for: summaries one at a time, so background work never crowds out live turns, and deliveries to products' servers four at a time on a runner of their own, so a slow product's server never holds up a summary ([D40](#d40)). A claim takes the job due soonest with `FOR UPDATE SKIP LOCKED`, so two claims never take the same one, and only of its runner's kinds, so an instance whose build has no handler for a kind leaves its jobs to one that has. That holds once every instance claims this way: an older one claims any kind and fails one it has no handler for, so the release that brings the claim by kind in replaces every older instance before any new kind is queued (one instance today). A kind's args only ever keep the fields they have: adding one makes a new kind, with a name of its own, since an older instance with the kind's handler refuses a field its type lacks and fails the job; claiming moves when the job may next run to the end of a lease (one minute, 30 s past a run's 30 s limit, itself twice the model service's 15 s deadline on a reply), so a job whose instance stopped mid-run is claimed again once the lease runs out. A run's result is saved only by the run holding the job, by its number, which only ever grows, kept apart from the count of runs that counted against the job. A failed run is tried again after a wait that starts at 10 s and grows four times each time, five counted runs in all for a summary, about 14 minutes, each runner setting its own number (eight for a delivery, [D40](#d40)), and no wait longer than a day; then the job fails, and is logged. A run that finds what it needs busy, as when the model service refuses at its own cap (a problem of type `/v1/problems/at-capacity`, told apart from the model's provider being overloaded, which is a failed run and waits longer each time), is let go without counting, to run again 5 to 10 s later at random, and a worker waits a poll's interval after any run that didn't succeed, so instances meeting a busy place don't use up each other's runs. Args that don't decode, or hold a field the kind's type lacks, fail the job at once, since no later run would read them otherwise. Stopping cancels the runs in hand and lets each job go, to run again at once, without counting the run. Jobs done are deleted by each runner, its own kinds only, once past its own keeping time (seven days for both today), when its instance starts and hourly after; failed ones stay, to be looked into. The runner imports no module: each module hands it a handler for its own kind, and `cmd/voice` wires the two. Running a job twice must be harmless, since nothing runs exactly once: each kind makes its result once per job, as a session has one summary.
> - Needed by: [U4](#u4); [NFR1 latency](#nfr1).
> - Assumes: the user doesn't wait for this work; PostgreSQL keeps up with the queue, a few writes per job, alongside everything else.
> - Why: a turn's time is the product; nothing slow belongs in it. One table with a kind and an `args` value keeps what kinds share, the claiming, the lease and the retries, written and tested once, while what each kind needs stays in its own module, typed; River, Oban, Solid Queue and Sidekiq all keep one queue of many kinds. Queuing in the same write is the outbox: a job is never queued for an end that rolled back, nor lost for one that committed. `SKIP LOCKED` is what PostgreSQL's documentation names for a queue several consumers read. Our own code, about 300 lines with its tests, does all one kind needs, without a library's tables and migrations beside goose's.
> - Trade-off: the database doesn't check a job's `args`, its kind's Go type does, so args that don't decode fail only when the job runs; kinds share one lifecycle, so a kind needing another (a recurring schedule, or priority over the rest; a priority column first) is the reason to split the table; claiming reads across workspaces ([D12](#d12)); a job of a kind no running build handles waits unclaimed, and unlogged; a job runs at least once, not exactly once; our own code to keep instead of a library's.
> - Alternatives rejected: River (pre-1.0, MPL-2.0, four tables and migrations of its own, LISTEN/NOTIFY and cron dependencies, for features one kind doesn't need yet); a queue in Redis (a second store, so the end and its job could disagree); a goroutine (a restart loses the work); a table per kind (the claiming, lease and retries written once per kind).
> - Status: accepted.
> - Enforced by: the runner's tests on a fake queue with a clock the test steps: a job runs with its args and is done, a failure runs again after each wait and fails after the last run, a wait grows four times up to a day whatever the attempt, args that don't decode fail at once, a runner leaves a kind it has no handler for waiting, and lets one handed over anyway go uncounted, a runner deletes only its own kinds' jobs done, a job whose lease ran out runs again and its first run's result is left out, a run past the last fails without running, stopping cancels the job in hand, waits for it and lets it go with the run not counted, workers run at once, only jobs done are deleted, a run that finds what it needs busy uses no attempt, and two workers sharing one busy place run every job in the end; against PostgreSQL, that a claim takes the job due soonest in any workspace, only of the kinds it asks for, claims at once never take the same job, a held job is due again once its lease runs out, a released one at once, one queued after another only once that one is done or failed, while that one is retried too, a run paused past its lease can't save once a later run was released, a retried job only after its wait, a failed one never, a run can't save another workspace's job, and only jobs done long enough ago, of the kinds given, are deleted; that ending a session queues its job in the same statement, with args the summary's handler reads strictly ([D38](#d38)); `cmd/voice`'s tests that a summary is claimed while every delivery worker is stuck on a slow product's server, and that a delivery runs the deliveries' attempts, more than a summary's.

<a id="platform-limitations"></a>
### Limitations and open questions

- Redis: unused until a measured cache needs it.
- Row-level security: when a workspace's product is run by someone other than the maintainer ([D12](#d12)).
- Guessed product keys: each well-formed guess costs one indexed read before it is refused, so someone sending many could load the database; rate limits by address come before voice-guide is reachable from the internet ([D32](#d32)).
- Which sites show a product's agents: any; an allowlist of each product's sites comes when products have dashboards ([D41](#d41)).
- Rotating the signing key drops every user: there is one key, read when an instance starts, so a rotation restarts every instance and stops every ticket. Key IDs in tickets, so instances check the old key and the new one side by side for a while, come when rotating without dropping users is needed ([D32](#d32)).

<a id="agents"></a>
## 7. Agents

<a id="agents-overview"></a>
### Overview

An agent is the AI a user talks to. It holds:
- its persona and rules;
- its activity: how a session runs;
- the context kinds it reads ([D27](#d27));
- how voice is set for it: pauses, maximum wait, and which text-to-speech voice it uses and how fast it speaks.

Which model answers is the agent version's own when its file names one, else a setting of the service; naming or changing it is a new version of that agent, and changing the service's setting moves every agent that names none ([D25](#d25)).

Agents are versioned: a running session keeps the version it started with.

<a id="agents-requirements"></a>
### Requirements

- <a id="w1"></a>W1. Create or change an agent. Caller: a member of the workspace. Excluded for now; today agent files are edited in the repository.
- <a id="w3"></a>W3. Put an agent on a product: the workspace's own product starts sessions with its agents through voice-guide's API, its server with the product's key, and the user's browser talks in them ([D32](#d32)); its server hears each session once it has ended, at the workspace's webhook ([D40](#d40)). Caller: a member of the workspace, through the product's server. Its keys and its webhook are set by scripts until members have logins; voice-guide's own web page is the first product.

<a id="agents-how"></a>
### How it works

<a id="d16"></a>
> #### D16. The reference agent: a mock interviewer
> - Decision: the first agent is a mock interviewer, practice for job interviews: one question at a time about the user's resume, with follow-ups, given only the resume, which the product sends as session context ([D35](#d35)). It is conversation only; it ends with a short summary of the questions covered and the weak spots.
> - Needed by: [U1](#u1); [U2](#u2); [U4](#u4); [D35](#d35) (tests session context from day one: one block, the resume).
> - Assumes: a placeholder resume in the repository stands in for real user data ([A3](#a3)).
> - Why: a real, demanding conversation that exercises every speech stage, with the simplest context.
> - Trade-off: features other agents need (documents, answer banks, grading) wait for those agents.
> - Alternatives rejected: an agent that needs many context kinds or a canvas first (more to build before the core loop works).
> - Status: accepted.
> - Enforced by: guidance only.

<a id="d17"></a>
> #### D17. Agents stored as versions that never change, published from one file per agent
> - Decision: an agent's versions are rows in PostgreSQL, in the agents module's own tables. A saved version is never changed or deleted; a change is saved as the next number for that agent. New sessions get the newest version; a running session keeps the one it started with, and every instance reads the same versions. Until members change agents themselves ([W1](#w1)), the repository holds one agent file per agent, `agents/<name>.md`, edited in place:
>   - a settings block at the top: the agent's ID, name, description, the context kinds it reads, the session context it expects from the product ([D35](#d35)), its summary section ([D39](#d39)), who speaks first ([D18](#d18)) and, optionally, the model that answers its turns, the service's own model unless set, and the voice its replies are spoken in and the speed (0.5 to 2, a round guess, 1 the voice's own pace), the service's own unless set ([D23](#d23));
>   - the persona below it, sent to the model as written, in plain prose that gives a reason with each rule.
>
>   The summary section is either the summary instruction alone, as a string, or a mapping: the `instruction`; optionally the `model` that writes the summary, the agent's own unless set; and optionally a `schema`, a JSON Schema of the agent author's own, written as YAML, for a structured part of the summary. Publishing checks the schema is a mapping whose top level is `type: object`, expressible as JSON and at most 16,000 characters encoded, a round guess with room for a few dozen fields; it is kept with the version as JSON with its keys sorted, so the same file always hashes alike. voice-guide gives the schema's fields no meaning of its own: an agent that should steer toward what it asks says so in its persona.
>
>   Publishing saves an agent file as a new version when its settings or persona differ from the newest version's. When an agent names a voice, publishing asks the text-to-speech service for its voices and refuses one it doesn't list; when the service can't be reached within 2 s, it publishes anyway and warns that the voices weren't checked, so publishing doesn't need the service running. It is the one way any workspace changes its agents, run whenever the workspace chooses and never tied to a deploy: today a command the workspace runs on its files, later an API call in front of the same rule ([W1](#w1)). An instance never publishes when it starts, so an instance still on an older build can't save its older text as the newest version. The maintainers' workspace is the first workspace, and the agents in `agents/` are its agents.
> - Needed by: [U1](#u1); [D24](#d24) (the agent layer of the prompt stays byte-identical, and so cached, for a session); [D27](#d27) (the context kinds an agent reads are fixed with its version); [W1](#w1) (members change agents without editing the repository).
> - Assumes: agents change far less often than sessions start.
> - Why: any instance may serve a session's next turn, so every instance must find the version the session started on, including during a rolling deploy, when instances run different builds; rows in one database are visible to all of them at once, and rows that are never updated keep a version fixed without a check on the repository. Versions, rather than a copy of the agent saved into each session, so versions can be compared, a change rolled back and every change traced. A version number counts within one agent and only the publish step assigns it; IDs stay UUIDv7 ([D7](#d7)). Voice-agent platforms keep agents the same way: Retell publishes versions that never change, and ElevenLabs versions every agent. A settings block above a prompt body is the common shape for agent and prompt files (Claude Code's subagents, Google's Dotprompt, Microsoft's Prompty).
> - Trade-off: the agent file shows only the text it will publish next; earlier versions are in the database. Publishing is a step between editing the file and new sessions using it.
> - Alternatives rejected: one file per version in the repository (during a deploy, instances on different builds hold different versions, and keeping a merged file unchanged needs a check against `main`); a copy of the agent saved into each session (no versions to compare or roll back to); publishing from every instance as it starts (an older build restarting mid-deploy would publish its older text as the newest version); a pointer to a chosen published version (no member to choose it yet; it is a small addition when [W1](#w1) comes); an API for members now (it needs a member identity, which comes with [W3](#w3)).
> - Status: accepted.
> - Enforced by: the loader refuses unknown settings, files that aren't `<name>.md`, two files with one ID and a folder with no agent files; a primary key on agent and version number, and a foreign key keeping each version in its agent's workspace; a database trigger, on even when ordinary triggers are switched off, refusing updates, deletes and truncation of versions, with tests that it does; saving locks the agent's row, with a test that saves at once never share a number; publishing checks every agent file before saving any, and refuses one reading a kind user context doesn't hold before reaching the database, with tests that it does, that unchanged settings and persona save nothing and that a change to any setting, the context kinds and the model included, saves a version; that the model reads back from the version, empty for one saved without; that publishing refuses an agent whose voice the service lacks, naming the agent and the voice, through the whole publish command, and publishes with a warning when the service can't be asked; that the voice and speed read back too, the speed exactly, and a voice that isn't a language and a style joined by an underscore, or a speed outside 0.5 to 2, NaN and infinity included, is refused when the file is loaded and again at publish; that a turn and a session's summary ask for their session's version's model, else the service's, a summary the model its summary section names before either, and a session started before a republish keeps its version's; that the summary section loads as a plain string or as a mapping, refusing a mapping without an instruction or with an unknown key, a summary model out of form, and a schema that isn't an object of `type: object`, can't be written as JSON or is over its cap, when the file is loaded and again at publish; that the summary model and schema read back exactly from the version, a large number included, and an unchanged schema saves no version while a changed or removed one saves one; the catalog reads the newest versions on every request and keeps exact versions in memory, with tests that a publish shows without a restart and that an exact version is read once, then served from memory only for its own number and workspace. Tests that a new session gets the newest version and that a session started before a change keeps its version, its turns speaking with that version's persona.

<a id="agents-use-cases"></a>
### Use cases

#### Put an agent on a product ([W3](#w3))

1. The product's page loads the drop-in client from voice (`/client/voice-guide.js`, a module script) and puts a `<voice-guide-session>` element where the conversation goes.
2. The product's server starts the session with its key ([U1](#u1)) and hands its page the session's ID and ticket, never the key; the page passes them to the element's `join({ session, ticket })`, with an `api` attribute naming voice when the script came from elsewhere ([D29](#d29), [D32](#d32)).
3. The element opens the session's audio connection at once and asks for the microphone, and an agent set to speak first opens the session ([D18](#d18)); the page sets the element's `screenInput` to what its screen shows, named text blocks, whenever it changes, and each change goes with the next turn ([D35](#d35)); the user talks or types, and the transcript, following the newest line unless the user has scrolled up to read, streams in as the page's does ([U2](#u2), [U3](#u3)). Mute stops the microphone, the user's way out when the agent's own voice leaks into it ([D37](#d37)); the element also holds it muted until the agent's greeting starts ([U1](#u1) step 6), and Mute shows only the user's own choice, which stays once the greeting starts.
4. The user presses End, or the product's page calls the element's `end()`, which resolves once voice ended the session and rejects when End didn't reach it: the audio closes, then the session ends ([U4](#u4)). The element fires `voice-guide-ended` with the reason, `ended`, `expired` or `unusable`, and, for `unusable`, `voice-guide-error` first with the refusal.

Failure modes, which voice-guide's own page, holding its session with the same code, meets the same way where they apply to it:
1. The microphone refused, missing, or its prompt left unanswered: the audio connection opens all the same, the agent is heard, typing is offered throughout; a refusal says how to allow it.
2. The audio connection fails or drops: typing goes on over HTTP while it is tried again with the same ticket, every 2 s for 30 s, then the element stays in text.
3. The ticket past its four hours, before the element joins or during the session: the element sends End once with it and says the session has expired; any other 401, or a 404 for a ticket that isn't the session's, says it can't continue ([D32](#d32)).
4. The product's page joins another session or ticket, even mid-reply, or removes the element: the element closes its audio connection, which cuts a reply where the user stopped hearing it, calls off a typed turn and a reconnect under way, and shows nothing more of that session, which ends when the product's server ends it or after it sits idle ([D38](#d38)); joins and moves in one go act once, on the last. An offer already being made still goes out, and its connection is closed once it opens.
5. Two elements on one page: each holds its own session; on one session, the newer takes its audio and the older goes on in text without taking it back, as a replaced tab does ([D19](#d19)); the script loaded twice defines the element once.
6. A page served over plain http: the browser offers neither the microphone nor the random IDs each request carries, so the element holds no session and says why.
7. The product's styles: a closed shadow root keeps them out, and the box is drawn on a frame inside, which inherits nothing, so a page rule matching the host, such as a reset of every element's padding and border, changes only the host's place on the page; the product styles the element through its custom properties and parts.
8. voice slow or down: typing is offered before the audio connection opens; a send that fails goes back in the box with its request ID kept ([NFR5](#nfr5)), and an End that fails leaves the session live to press again.
9. The browser plays the agent only after the user has interacted with the product's page: until then the element offers a press to hear the agent, and asks for the agent's opening only once it plays, so the greeting is heard from its first word; a product that joins from the user's own click never shows it.
10. A ticket set as an attribute by mistake: removed at once and never used, with a console warning naming `join` ([D29](#d29)).
11. A refusal that isn't voice's own, such as a proxy's error page: told by its status alone, never its body.
12. A block of screen input voice refuses, as over its limit: the element fires `voice-guide-screen-refused` naming the block and why, and the session goes on without it.

<a id="sessions"></a>
## 8. Sessions

<a id="sessions-overview"></a>
### Overview

A session is one conversation between a user and an agent version. It starts with the agent's context loaded, and the agent's opening when it speaks first, runs turns, and ends with what it learned saved in the background. An ended session can be resumed.

<a id="sessions-requirements"></a>
### Requirements

- <a id="u1"></a>U1. Start a session with an agent. Caller: a user.
- <a id="u4"></a>U4. End a session; what was learned is saved in the background. Caller: a user; then a background job.
- <a id="u5"></a>U5. Resume an earlier session. Caller: a user.

<a id="sessions-how"></a>
### How it works

<a id="d18"></a>
> #### D18. An agent that opens warms the model's cache with its opening
> - Decision: an agent's file says who speaks first in its sessions, `opens: agent` or `user`, `user` unless set, kept with the version ([D17](#d17)). In a session whose agent opens, the page asks for the opening: a turn marked `opening`, with no words (screen input allowed), sent on the data channel once the audio connection is up and the browser plays it, so it is heard from its first word, or over HTTP once audio has failed or isn't used, so it is shown ([D11](#d11)). voice takes it only for a version that opens and only as the session's first turn. The core checks the version, and that no turn of the session has finished, before the model is asked; the statement that starts the turn checks that the session has no turn at all, so two instances or two tabs never both take turn 1, and an opening sent while a turn is still in flight is refused there, its reply stopped. A refused opening is answered 409 of type `/v1/problems/opening-refused`, which the page lets go. The model gets one fixed cue in the user's place and writes the greeting from the agent's instructions and what the session was given. The opening is saved as turn 1 with no words, and every later prompt holds it again as the cue and the greeting, byte for byte, so its request warms the cache for the user's first turn. An agent that waits gets no warm-up for now.
> - Needed by: [NFR1 latency](#nfr1); [D24](#d24).
> - Assumes: the provider caches a prompt prefix for at least the gap between the opening and the user's first turn.
> - Why: the opening is a request the session wants anyway, so the warm-up costs nothing more, and an interviewer, say, starts the conversation itself. The page sends it, rather than voice at the start, so it comes once the user can hear it, on the server holding the audio; a page reloaded sends it again and is refused, so nothing is said twice. A cue rather than fixed text lets the greeting use what the session knows, such as last time's notes.
> - Trade-off: an agent that waits pays the full read on the user's first turn; a session left before the user says anything still costs the opening's billed request; the user waits for the model's first words before hearing anything.
> - Alternatives rejected: a warm-up request at every start (a billed request nobody hears, for every agent); voice opening when the session starts (no one may be listening, and the greeting would wait for the audio connection); fixed opening text in the agent file (it can't use what the session knows; added when an agent needs it).
> - Status: accepted.
> - Enforced by: the agent file loader refuses `opens` other than `agent` or `user`; tests that an opening is turn 1 with no words and the next prompt continues it byte for byte; that it is refused before the model is asked once a turn has finished, for a version that waits, or with words, and with another session's ticket as any turn is; that with a turn still in flight it is refused when the turn starts, its reply stopped; that two openings at once take one turn, also against PostgreSQL; that one sent on the data channel while another turn is taken doesn't interrupt it; that a greeting cut short reaches later prompts as far as it was heard, and a failed one is saved lost; that a session where only the agent spoke gets no summary, and its delivery holds the opening as a turn with no words.

<a id="d35"></a>
> #### D35. The product sends what it knows when a session starts, and what its screen shows with each turn
> - Decision: context reaches an agent from three places, by who holds the truth:
>   - session context: what the product already knows about the user (a resume, an order history, a plan), sent by the product when it starts a session, as named text blocks. The agent's version declares the names it expects; a start missing one, or sending one the agent doesn't declare, is refused. The blocks are recorded on the session and sit in the cached part of the prompt for the whole session;
>   - screen input: what the user is doing in the product right now (the page, a board, an attached file), as named text blocks, recorded with the turn it goes with; the newest block of each name is placed in the part of the prompt that is new each turn, and earlier ones stand as a fixed line where they changed ([D24](#d24)). A typed turn over HTTP carries it; while the audio connection is up, the page sends each block as it changes and it goes with the next turn taken on that connection, spoken, typed or the agent's opening ([D11](#d11));
>   - user context: what voice-guide itself learns about the user in conversations, such as session summaries ([D27](#d27)).
>
>   The product picks what is relevant (the current resume, the chapter in focus); voice-guide checks names and sizes and places each block, and doesn't search or cut. A session fact that changes mid-session arrives with a turn as a note in the conversation, so the cached part stays the same. A start's session context has a size limit in characters, which also bounds each block, and the request body a limit that always fits it. Session context comes from the product's server, which starts the session with its key ([D32](#d32)), so the user can't change what the product says about them; screen input comes with each turn from the user's browser, as trusted as the user.
> - Needed by: [U1](#u1); [U2](#u2); [D24](#d24) (each kind lands in its own layer); [NFR9 embeddability](#nfr9) (a product adds its agents and its screen, nothing more).
> - Assumes: the product already holds what it knows about its users, and its own copy is the one kept current.
> - Why: the product is the source of truth for its users and its screen; a second copy in voice-guide would drift and need syncing and deleting. Agent platforms do the same: ElevenLabs takes dynamic variables at the start of a conversation (every one the agent defines is required) and contextual updates during it; Vapi and Retell take variables when a call is created; OpenAI's Realtime API injects items mid-session. Screen input travels on its own as it changes, rather than with each turn, so a spoken turn, which the browser never sends, carries it too, and only what changed travels.
> - Trade-off: a product with no store of its own must keep, or resend, what it knows each session; voice-guide records each session's copy. Screen input sent ahead waits only on the server holding the connection, so the page keeps what no turn has taken and sends it again after a reconnect.
> - Alternatives rejected: voice-guide storing what the product knows about its users (two copies to keep in step); voice-guide fetching it from the product through connectors (each product a new integration, before any needs it); substituting values into the agent's text with placeholders (named blocks keep the agent's text fixed and cacheable).
> - Status: accepted.
> - Enforced by: the agents module refuses badly formed or repeated names in an agent's declaration; starting a session refuses context that is missing, not declared, sent twice or empty (422 of type `/v1/problems/session-context-mismatch`, naming each) and context over the character limit (413), and saves nothing then; each session records its context; a turn refuses screen input with a badly formed name (lower-case letters, digits and _, at most 32, as for session context), a name that frames the summary's prompt (`instructions`, `transcript`, `screen`), a name sent twice, an empty or unstorable block, or more than its character limit, before anything is saved or sent; speech refuses screen input sent ahead by the same rules, the blocks it holds counted, when it arrives; all with tests. Speech's tests that screen input sent ahead goes with the next turn taken, spoken, typed or the opening, the newest block of each name, a turn's own block winning, that a refused turn or one taken back leaves it for the next, and that a block counts against the limit until a turn carrying it is saved; the drop-in client's tests that only changed blocks are sent, the newest of each name once a full buffer drains, a change no turn took is sent again after a reconnect, and a turn over HTTP carries the blocks still pending but none voice refused.

<a id="d38"></a>
> #### D38. A session ends when the user ends it, or after five minutes idle
> - Decision: a session is live until it ends, and it ends once, by whichever comes first:
>   - the product ends it: `POST /v1/sessions/{id}/end`, answered 200 with the session and when it ended; ending it again changes nothing and answers the same;
>   - it goes `VOICE_SESSION_IDLE` (5 minutes unless set; voice refuses one no longer than a turn may run) idle: no turn started in it and none of the user's speech heard, counted from its start for a session with neither. The session's row keeps when it was last active: a turn starting marks it in the same statement that starts the turn, holding the row until the turn has started, and speech marks it when an utterance of the user's speech begins, at most once every tenth of the idle time per audio connection and at least every 30 s, so a user answering at length after a long think isn't idle. Every instance looks once a minute and ends every idle session in one statement; one waiting on a turn starting checks the session again once the turn has marked it, so a session never ends under a turn that just started, and instances looking at once end each session once.
>
>   An ended session takes no more turns and gets no audio connection: a turn, typed or spoken, or an audio offer is refused with 409 of type `/v1/problems/session-ended` (on the data channel, `refused` with the reason `ended`), and the server holding the session's audio connection ends it with the reason `ended` once its next turn is refused, so no message passes between servers. A turn in flight when the end lands finishes and is saved: a turn starting holds the session's row, so an end waits for it, and a turn waiting on an end is refused. The page's End closes its audio connection first, which cuts a reply still playing where the user stopped hearing it, as an interrupt does ([D11](#d11)), then ends the session. A retry of a turn the session took before it ended is still a repeat, so the caller counts it as taken. A dropped connection doesn't end a session, and a session's length has no cap.
> - Needed by: [U4](#u4); [D15](#d15) (the end is what a session's background work waits for).
> - Assumes: five minutes with no turn and no speech means the user has gone; someone thinking silently longer than that starts a new session, or resumes this one once resuming is built ([U5](#u5)).
> - Why: voice platforms end a session by an end from either side, a silence or idle time, a length cap, or a mix: Retell after 10 minutes of silence by default, Pipecat after 5 minutes idle, ElevenLabs not at all unless set; caps of 10 minutes at Vapi and ElevenLabs, an hour at Retell and in OpenAI's realtime API. A closed tab never presses End, and a page can't reliably say it is closing (`pagehide` and `unload` don't always fire, on phones least of all), so a time limit kept by the server is the only guarantee. Five minutes is Pipecat's default, a starting value checked once real sessions show how long people pause. Keeping when the session was last active on its own row, written by the turn starting, makes the turn, the end and the sweep take the same lock, so each sees the others' writes; reading the turns instead let a sweep end a session whose turn had just started. Marking speech at most every tenth of the idle time keeps nine tenths of it ahead of a user still talking at any setting; capped at 30 s (a round guess), it is a write per talking user every 30 s at most. Learning of the end from the next refused turn spares the servers passing messages: a connection on an ended session sits silent until its user speaks.
> - Trade-off: a user who steps away for five minutes finds the session over; an abandoned audio connection keeps its place on its server until the browser goes, about 30 s after the tab closes, or its user speaks; a session ends up to a minute past its idle time, or up to a tenth of it short, counted from the user's last speech; every turn writes the session's row as well as its own.
> - Alternatives rejected: End alone (a closed tab's session would never end); ending when the audio connection drops (a reload or a network blip would end a live session, and typed sessions have no connection); a length cap (it would cut an interview off mid-question, with nothing yet to protect; one comes with limits on cost, if they need it).
> - Status: accepted.
> - Enforced by: tests that ending a session marks it ended once, keeping the first time, and only the caller's own; that a turn in an ended session is refused before the model is asked, typed over HTTP with its problem type and on the data channel, and spoken, where the refusal ends the audio connection, while a retry of a turn taken before the end is a repeat; that an ended session gets no audio connection; that a turn in flight when the end lands is saved; that hanging up mid-reply saves it cut where the user stopped hearing it; that the user's speech marks the session active; against PostgreSQL, that a turn waiting on an uncommitted end is refused once it commits, that a session ends at its idle time and not a second before, counted from its last activity, a turn starting or a touch, in every workspace, that a sweep waiting on a turn starting leaves the session live, and that two sweeps at once end each session once; that the sweeper runs every interval until voice stops, a failed sweep warned of and tried again; the page's tests that End closes the audio before ending, that a turn refused as ended, an audio connection ended as ended and an audio offer refused as ended each show the session over.

<a id="d39"></a>
> #### D39. A session's summary: written by the model its agent names, with the agent's instruction, read back only by that agent
> - Decision: when a session ends, its summary job ([D15](#d15)) writes what the session learned and saves it through user context as a `session_summary`, for the agent's next session with the user:
>   - each agent sets what goes in it with a summary instruction in its agent file ([D17](#d17)); an agent without one gets no summary;
>   - it is free text, about 150 words as the instruction asks, cut at the last sentence within 2,000 characters if longer, so ten fit what a session loads;
>   - the model the agent's summary section names writes it, else the model answering the session's turns; a model either names is fixed with the agent version, so a session's summary is written on its own version's, and the service's setting otherwise; asked outside any session, so the model service keeps nothing for it, and as background work: the model service runs one such ask at a time, within its cap, so live turns always keep its other places ([D25](#d25)); the prompt is the conversation as the user heard it, an interrupted reply as far as they heard it ([D36](#d36)), each screen input block as the marker where it changed, then the newest block of each name whole, once, inside a fixed `<screen>` tag ([D24](#d24)), then the instruction, so a board that changes on most turns doesn't grow the request past its size limit; the model is told only the text inside `<instructions>` is instructions, the transcript and the screen being what the user said and showed, and screen input can't take the names of those tags, so a user can't write instructions for their own summary;
>   - it is written only for a session in which the user said or sent something, once no turn of it is in flight, and once per session however often the job runs;
>   - it is saved with the agent that wrote it, the session it is of and when that session ended, and only that agent loads it, the latest session's first, whenever each was saved, with whatever else it reads ([D27](#d27), [D34](#d34)); each sits in the prompt dated by its session's end, so "last time" is the latest session. A correction replaces it as any item is replaced. The session's product hears it too, with the session's delivery ([D40](#d40)).
> - Needed by: [U4](#u4).
> - Assumes: one free-text summary is what the next session needs; the agent that wrote it is the one that knows what it is for.
> - Why: what is worth remembering differs per agent, so the voice platforms set the post-call prompt per agent (Vapi's default asks for "2-3 sentences"; Retell and ElevenLabs add typed fields); only the next session reads it, as text, so typed fields wait for something that reads them as data, such as grading or the past-sessions view. Retell and ElevenLabs let a separate model write it; here the agent's author may name one in the summary section, a stronger one when the summary asks for more, since nobody waits for it. A cut summary beats none: a model that ran long once would likely run long again, and the opening carries the most. A summary another agent reads would be read for a purpose it wasn't written for; letting one is a deliberate change.
> - Trade-off: the summary arrives some seconds after the end (measured with `.claude/skills/measure/check-summary.sh`: 3 to 4 s with Haiku 5.5, 5 s with Sonnet 5.5), so a session started at once may open without it; a summary job running while a turn is still in flight waits for it and runs again; each summary is a billed model call; with several sessions ending at once, summaries queue behind each other. A known limit: a product's server ending the session while its reply still plays on a page held by another instance summarises that reply whole, since the cut is saved only when the page stops hearing it; the page's own End closes its audio first, so its cut is saved before the end.
> - Alternatives rejected: one summary prompt for every agent (an interviewer and a tutor note different things); structured fields now (nothing reads them yet); summaries read by every agent of the workspace (no purpose limit); the server choosing a cheaper model for every summary (little saved on a subscription, and the agent's author knows what its summary needs).
> - Status: accepted.
> - Enforced by: tests that ending a session, or its idle end, queues its summary once; that the summary is written from the turns, an interrupted reply as far as it was heard, each screen change as its marker and the newest block of each name once at the end inside `<screen>`, with the agent's instruction, asked outside any session, saved once per session and loaded only by the agent that wrote it, by its next session; that the latest session's summary comes first, dated, even when saved before an older one's; that ending after the page hangs up mid-reply summarises the reply only as far as it was heard; that none is written when the user said nothing, the session never ended, there is no such session, or the agent asks for none; that a run fails to run again while a turn is in flight, when the model fails or when user context can't save, and saves a turn left in flight past the deadline lost before reading the turns; that a summary over the cap is cut at its last sentence within; user context's tests, against PostgreSQL, that a session has one summary however many saves race while a correction replaces it, that the table refuses a summary without its agent, session and end, that summaries come latest session first, and that each agent reads only its own; the model service's tests that asks outside any session run one at a time while live replies keep the other places, that the place frees once such an ask fails or is cancelled, and that it refuses to start with fewer than two places; that the model having no place makes the summary's run busy, not failed; `.claude/skills/measure/check-summary.sh` against the real model.

<a id="d40"></a>
> #### D40. A product's server hears each ended session at its webhook: signed, after the summary, the same delivery ID on every attempt
> - Decision: a workspace may have a webhook, a URL on its product's server and a secret, set by `scripts/webhook.sh` until members have logins. Once a session ends, voice-guide posts it there:
>   - as JSON in the shape the Standard Webhooks specification gives: `id`, the delivery's ID; `type`, `session.ended`; `timestamp`, when the session ended; and `data`, the session: its `id`, `user` (the product's own reference for the user, never voice-guide's user ID), `agent_id` and `agent_version`, `started_at` and `ended_at`, its `turns` in order (`seq`, the user's `words`, the agent's `reply` as far as the user heard it, and whether it was `cut`), `turns_left_out`, and its `summary`, null when none was written ([D39](#d39));
>   - signed by voice's core with the workspace's secret, `whsec_` and 32 random bytes in base64, by HMAC-SHA256 over the delivery's ID, the attempt's time in seconds and the body, joined by full stops, sent in the headers `webhook-id`, `webhook-timestamp` and `webhook-signature` (`v1,` and the HMAC in base64). The secret is shown once, when it is made with the first URL or rotated, and kept readable, since signing needs it; setting another URL keeps it;
>   - by a background job of its own ([D15](#d15)), queued in the same write as the end when the workspace has a webhook not stopped, which runs once the session's summary job is done or has failed for good, and builds the body at its run, so turns saved after the end, such as a reply cut where the user stopped hearing it, are in it when saved before it runs;
>   - with one delivery ID on every attempt, so the product's server drops a repeat; each attempt goes to the URL and is signed with the secret the webhook has then, and waits at most 15 s for an answer, 5 s of it to connect;
>   - to an `https` URL, or an `http` one only on voice-guide's own machine (localhost or a loopback address), for a product run beside it, checked when set and again before each attempt, so a URL kept before the rule fails the delivery rather than post it in plain text;
>   - on a runner of its own, four deliveries at a time per instance, so a slow product's server holds up other products' deliveries at most, never a summary ([D15](#d15));
>   - a 2xx answer delivers it. 410 Gone stops deliveries to the webhook until its URL is set again: that delivery, and each one after while it is stopped, fails and is logged with its session's ID. Any other answer, a redirect included, which isn't followed, and no answer at all are tried again, the waits growing four times from 10 s, eight attempts over about 15 hours, then the delivery fails. A delivery is logged with the answer's status, never the URL or the body;
>   - a body holds at most 256 KiB: a longer session's earliest turns are left out, counted in `turns_left_out`, since the end is where the session got to and the summary covers the rest.
> - Needed by: [W3](#w3); [U4](#u4); [NFR9 embeddability](#nfr9).
> - Assumes: a product's server answers within seconds and does its own work afterwards; an answer it gives now, other than 410, may change, as during its deploy or before it holds a rotated secret; it checks the signature and that the timestamp is recent, and keeps the delivery IDs it has seen long enough to drop a repeat.
> - Why:
>   - The product embeds the agent and answers for its users: it wants what each session covered, to show it or act on it, without polling voice-guide for sessions that ended.
>   - Standard Webhooks is a published shape with verifying libraries in many languages, so a product checks a delivery with code it already has. HMAC with a shared secret is what Stripe, GitHub and Svix sign with; signing the ID and the time with the body lets the product refuse an old delivery sent again by someone who caught it. Signing sits in voice's core with the rules, since it is the contract products verify; the outbound adapter only posts, and is a port with one real implementation so the network stays out of the core's tests.
>   - Queued with the end, it is the outbox again: the delivery exists exactly when the end commits. Waiting on the summary job, rather than being queued when that job settles, leaves no moment where a crash loses it, and the runner needs no rule about which kind follows which.
>   - Built at its run, it holds the turns as the user heard them, including a cut the page saves after the end, if saved before the delivery runs.
>   - The same ID on every attempt makes a retry safe: a job runs at least once, not exactly once.
>   - Eight attempts over about 15 hours carry a delivery through a product's deploy or an outage of hours, where the five a summary gets would give up after 14 minutes; 15 s is the low end of the specification's 15 to 30 s (GitHub gives 10 s), so a run still reads the session and saves its result within its 30 s.
>   - 410 Gone is how the specification says a product takes a webhook down. Every other refusal is tried again, as the specification and Stripe treat any answer but 2xx: a 401 or 404 can come from a product mid-deploy or before it holds a rotated secret, and is gone in minutes. A redirect isn't followed, since that would post the session and its signature wherever the product's server pointed; a URL should be the one that answers.
>   - The product's own reference names the user, since that is the only name it knows them by.
>   - 256 KiB holds about two hours of conversation at about 1 KB a turn, a turn every half minute, and stays well under the 1 MiB web servers commonly accept by default. A round guess, measured once products receive real sessions.
> - Trade-off:
>   - A delivery the product's server will never take, such as one over the size it accepts, is posted eight times over 15 hours before it fails.
>   - The secret is kept readable, unlike a product key's hash: someone with a copy of voice-guide's database can sign deliveries a product takes as voice-guide's, telling it of sessions that never happened. They can't act as the product toward voice-guide. Encrypting it at rest is deferred.
>   - A delivery arrives after the summary, some seconds after the end, and with no ceiling when the summary job waits: up to 14 minutes of retries, and longer while the model service stays busy, since a summary let go as busy uses no attempt.
>   - Rotating stops the old secret at once, so a delivery signed with the new one before the product has it is refused, then tried again, and delivered once the product has it, within the 15 hours.
>   - A body can be far larger than the 20 KB the specification suggests, since the product's server reads the whole session again later, if it wants it ([U6](#u6)).
>   - Many deliveries to one slow product's server can hold all four delivery workers of an instance, 15 s an attempt, so other products' deliveries wait behind them; summaries don't.
> - Alternatives rejected: the product polling for ended sessions (a loop on the product's side, for what the delivery already carries; the past sessions the product can read ([U6](#u6)) catch up on a delivery it missed); a delivery naming only the session, the product fetching the rest (a second call for every ended session, when the one delivery can carry it; the product's server reads past sessions only when it wants a history ([U6](#u6))); building the body at the end (a cut the page saves after it would be missing); queuing the delivery once the summary job settles (a crash between the two would lose it); following the specification's schedule of days (the waits grow four times, so a ninth attempt would come almost two days after the eighth; waits with a ceiling come when an outage shows the need); signing with both the old and the new secret for a while after a rotation, which the specification's list of signatures allows (the retries close the gap; deferred until products rotate routinely); failing a 4xx at once (a product mid-deploy or mid-rotation would lose deliveries for good); deliveries on the summaries' worker (a slow product's server would hold up every workspace's summaries); voice-guide's user ID in the body (the product doesn't know it, and shouldn't).
> - Status: accepted.
> - Enforced by: tests that a delivery carries the session, the product's reference and never voice-guide's user ID, its turns as heard and its summary, and a session with no turn an empty list of turns and a null summary; that a session whose user has no reference is never delivered; that a cut saved after the end is in it; that every attempt posts the same delivery ID, to the URL and with the secret the webhook has then; that a delivery is signed so the webhook's secret verifies it, at the attempt's time, and that the signing matches the specification's published example; that a URL kept before the rule is never posted to; that 2xx is delivered, and every other answer but 410, a redirect and 4xx included, and no answer are tried again; that 410 stops deliveries until the URL is set again, and only while the URL is still the one that answered, each delivery not made naming its session, never the URL; that a workspace with no webhook is posted nothing; that the earliest turns are left out to fit, the body exactly at the limit keeping every turn; that setting a URL makes the secret once and moving it keeps it, rotating replaces it, and a URL that isn't an https address with a host, or http on this machine, is refused; that the secret prints, logs and encodes redacted; the adapter's tests, against a test server, that a delivery is posted as JSON with the headers the core signed, that a redirect is answered as it is and nothing posted where it points, that connecting is given up after its own timeout, well within the attempt's, and that no answer within the deadline and no connection are errors that name no address; against PostgreSQL, that ending a session queues one delivery after its summary job however many ends race, only for a workspace with a webhook not stopped, and that two idle sweeps at once queue one per session, for its own workspace's webhook; `cmd/voice`'s tests that a delivery to a webhook gone fails at once and one refused otherwise runs again, that a summary is claimed while every delivery worker is stuck on a slow product's server, and that a delivery runs eight attempts, more than a summary's.

<a id="sessions-use-cases"></a>
### Use cases

#### Start a session ([U1](#u1))

Steps 1 to 6 are built, and failure modes 2 and 5; the rest is proposed.

1. The user picks an agent on the product's page, and the product's server asks voice-guide to start the session with its key, its reference for the user, and the session context ([D32](#d32)); a different way of running a session is a different agent.
2. voice creates the session: ID, workspace, user, agent and the newest version of it, start time, the session context the product sent, which must match what the version declares ([D35](#d35)), and the voice and speed its replies are spoken in: the start's, else the version's, else the service's, each of the two chosen on its own, so a session that is read back later speaks as it started ([D23](#d23)). A user is added with the first session the product's reference names them, in the same write; the reference is the product's own stable ID for the user, not a name or an email address, compared exactly as sent: one that is empty, over 200 characters, has a space at either end, or holds anything but printable characters on one line (no line or paragraph separators, no invisible format characters) is refused (400). The agent must be one in the product's workspace; an unknown agent and another workspace's are refused alike. voice answers with the session (its ID, agent, agent version, voice, speed and start time, and when it ended, for a repeat of an ended one) and a ticket for it, issued then, which the product's server hands to the user's browser, or, past the session's four hours, no ticket, and the product starts a new session; no cache may keep the reply ([D32](#d32)).
3. user context loads what the version may read about the user: the context kinds it declares, within the limit ([D27](#d27), [D34](#d34)). The session records the IDs of what was loaded, never the text, so every turn rebuilds the same user-context layer whichever instance serves it. An agent that reads nothing about the user starts without asking. If the load fails, nothing is saved and the same request can be sent again. voice builds the prompt's agent layer and user-context layer from these.
4. An agent that opens warms the model's cache with its opening, in step 6; one that waits gets no warm-up for now ([D18](#d18)).
5. The audio connection opens; the browser asks for the microphone.
6. An agent that opens speaks first: once the audio connection is up and the browser plays it, or the connection has failed, the page sends the opening, the session's first turn, with no words, and the greeting is heard, or shown. From the session's first audio connection set up, before the opening is sent, until the greeting's first sound, or the opening ends without one (refused, lost, failed, cut short by a typed turn, or the audio closed), the microphone sends silence, as Mute does, and the status line says the agent is about to greet the user; then it opens again unless the user muted it, and after 30 s at most. Speech in the wait before the greeting's first sound would otherwise cut it at its first packet, so the user would never hear it; once it sounds, the user can cut in as on any reply. A reconnect once the session has turns holds nothing. Typing works throughout. An opening the session doesn't take, as after a reload, once the user has spoken first, or for an agent that waits, is refused and let go, and the agent waits for the user.

Failure modes:
1. The agent doesn't exist, or is another workspace's: refused, both alike; the agent list shows only the workspace's agents.
2. Microphone permission denied: the session starts in typing mode and says how to allow the microphone. A microphone missing, or a prompt left unanswered, leaves typing too, without a note.
3. The audio connection fails: typing mode; it reconnects in the background.
4. More context about the user than the size limit: user context loads the most relevant within the limit; what was left out is visible to the user ([U7](#u7)).
5. The opening fails: the model fails, is busy, or is slow past the turn deadline: it is saved lost as any turn, the session stays usable, and the user's first turn carries it ([U2](#u2) failure mode 5).
6. A live session with this agent already exists: offer to resume it ([U5](#u5)) or start a new one.
7. Start pressed twice: one session, recognised by the request ID the caller sends, unique per user, so no one reaches another user's session by sending their request ID ([NFR5 safe retries](#nfr5)). A repeat must ask for what the first start did, the same agent and the same session context (names and contents, in order), and gets the same session back as it was, with a fresh ticket. The voice and speed a repeat names aren't compared: the first start's are kept. A request ID already used with another agent or with different session context is refused (422, as the IETF draft on idempotency keys answers a key reused with another payload) and changes nothing, so a client's mistake never lands the user in the wrong agent's session or in a session whose session context isn't what the product last sent.
8. The agent changes while sessions run: running sessions keep their version ([D17](#d17)).
9. User context slow or down: an agent that reads nothing about the user still starts; one that reads something is refused with 503, and the same request can be sent again, since its conversation depends on that context. A repeat of a start that already succeeded gets its session back without asking user context. A per-agent choice to start anyway comes with the first agent that is useful without its context.
10. Many sessions start at once: starting is cheap; warm-ups are rate limited (later).
11. The product's session context doesn't match what the agent's version declares (missing, not declared, sent twice, empty) or is over the limit: refused, saying which, and nothing is saved ([D35](#d35)).
12. The product's key is revoked, unknown or malformed: refused (401) before anything is read but the key's hash; the database can't be reached to check it: 503, so the product retries the same request rather than replacing a key that works ([D32](#d32)).
13. Two starts name a new user reference at once: one user is added, and both sessions are that user's; pressed twice, with one request ID, they make one session. A user is added only with their first session, in the same write, so a refused start adds none.
14. The user reference can't be looked up: 503, nothing is saved, and the same request can be sent again. The key check, this lookup and the voices list a start naming a voice needs ([U1](#u1) failure mode 17) are the only failures a start answers 503; any other failure past the key check is a 500, as on every route.
15. A ticket sent to start a session or list agents: refused (401); only the product's key starts a session ([D32](#d32)).
16. A start names a voice or a speed the service can't take: a voice that isn't a language and a style joined by an underscore (`af_heart`), or a speed outside 0.5 to 2, is refused (422, type `/v1/problems/voice-settings`) before anything is asked of the text-to-speech service; a voice it doesn't list is refused (422, type `/v1/problems/unknown-voice`). Nothing is saved. An agent file's voice the service doesn't have is refused when the agent is published, when the service can be reached then; when it can't, the agent publishes with a warning that its voice wasn't checked, and each phrase of a session with it fails at the first reply.
17. The text-to-speech service can't be reached, or doesn't answer within 2 s, to check a voice the start names: refused with 503, and the same request can be sent again. A start naming no voice never asks it, so a session in text depends on it no more than before; a repeat of a start that succeeded gets its session back, with the voice and speed of the first, without asking.

#### End a session ([U4](#u4))

Built.

1. The product ends the session, as the page's End button does with its ticket, or its server with its key ([D32](#d32)), or the session goes five minutes with no turn and no speech ([D38](#d38)).
2. The session is marked ended, once: turns and audio connections are refused from then on, and a turn already in flight finishes and is saved.
3. A background job ([D15](#d15)), queued in the same write as the end, asks the model for what the session learned, as the agent's summary instruction says, and saves it through user context as a session summary, which only that agent's next session with the user loads ([D39](#d39)).
4. When the workspace has a webhook, a second job, queued in the same write, waits until the summary job is done or has failed for good, then posts the session, its turns as heard and its summary, signed, to the product's server ([D40](#d40)).

Failure modes:
1. End pressed twice, or sent again after a lost reply: the session ends once, and the answer is the same.
2. A turn in flight when the end lands: it finishes and is saved. A turn starting at that moment is either in flight first, and saved, or refused.
3. The user closes the tab without ending: the session ends after five minutes with no turn and no speech.
4. End pressed while the agent speaks: the page closes its audio first, which cuts the reply where the user stopped hearing it, saved as an interrupted reply is, then ends the session.
5. A turn sent again after the end, by a client retrying a turn the session took just before: refused as a repeat, so the client counts it taken, not as ended.
6. The session ends elsewhere, by the product's server or after sitting idle, while its page is open: the page's next turn, typed or spoken, is refused, the audio connection is ended with the reason `ended`, and the page says the session has ended.
7. A turn or an audio connection asked for in an ended session: refused with 409 of type `/v1/problems/session-ended`, which a client tells from another turn in flight; the page stops asking for audio at once.
8. The model is down, fails or is busy with another summary, or user context can't save: the job runs again after a wait growing from 10 s, five runs in all, then fails and is logged; the next session opens without the summary.
9. A turn still in flight when the job runs: it runs again once the wait is over, so the summary holds the turn as saved; a turn left in flight past the turn deadline, by a crash say, is saved lost, as the session's next turn would save it, and its words go in the summary.
10. The user said nothing in the session, or the agent asks for no summary: none is written.
11. The job runs twice, as when its instance stopped mid-run: the session keeps its first summary.
12. The model writes past the cap: the summary is cut at its last sentence within it.
13. The workspace has no webhook, or a 410 Gone stopped its deliveries: no delivery is queued, and one already queued posts nothing.
14. The product's server is slow or down, or answers anything but 2xx or 410, a redirect included, which isn't followed: the delivery runs again after a wait growing from 10 s, eight attempts over about 15 hours, then fails and is logged. A product mid-deploy, or refusing the signature until it holds a rotated secret, gets it once it is back.
15. The product's server answers 410 Gone: deliveries to its webhook stop until its URL is set again; this one and each queued after it fail, logged with their session's ID.
16. A delivery runs twice, as when its instance stopped after posting and before saving: the product's server gets the same delivery ID and drops the repeat.
17. The URL moves or the secret is rotated between attempts: each attempt goes to the URL and is signed with the secret the webhook has then.
18. The summary job fails for good: the delivery goes once it has, with the summary null.
19. A session too long for a delivery: its earliest turns are left out, and counted.
20. Many sessions end at once for one slow product's server: their deliveries hold the instance's delivery workers, 15 s an attempt at most, and other products' deliveries wait behind them; summaries run on their own worker and never do.
21. A ticket is stolen: the product's server ends the session with its key, and the ticket's turns and audio offers are refused from then on, as in any ended session; another workspace's session is answered 404 ([D32](#d32)).
22. End pressed within 15 minutes after the ticket's four hours: the session still ends at once and its summary runs; the audio connection had already closed at the cap, the reply cut where the user stopped hearing it. Later, End is refused as expired and the idle sweep ends the session ([D32](#d32)).

#### Resume a session ([U5](#u5))

Its flow and failure modes are designed when it is built.

<a id="sessions-limitations"></a>
### Limitations and open questions

What happens when user context is slow or down at a start is settled in failure mode 9 above. Of deliveries to a product's server ([D40](#d40)):
- A webhook URL isn't checked against private and internal addresses, so an `https` URL could make voice-guide post to a service inside its own network; only the repository's script sets one today, and the check comes before members set their own.
- The secret is kept readable, so a copy of the database lets someone forge deliveries to that product; encrypting it at rest comes with members managing their own webhooks.
- A rotation has a gap: deliveries signed with the new secret before the product has it are refused, then delivered by a later attempt once it has it. Signing with both secrets for a while comes when products rotate routinely.
- A delivery waits on its summary job with no ceiling: a summary let go while the model service is busy uses no attempt, so a model service busy for hours holds the delivery for hours. A ceiling comes if that happens in use.
- A cut the page saves after the delivery has run, from a server holding its audio connection on another instance, isn't in the delivery, as it isn't in the summary ([D39](#d39)).
- One slow product's server can hold every delivery worker of an instance; a limit per product comes when several products share an instance.
- A `Retry-After` header on a 429 or 503 isn't read: the next attempt follows the usual wait.

<a id="turns"></a>
## 9. Turns

<a id="turns-overview"></a>
### Overview

A turn is one exchange: the user speaks (or types), and the agent answers out loud. Its path is the speech pipeline ([section 4.5](#turn-life)). Each stage sits behind its own swap point ([D2](#d2)) and is timed ([D13](#d13)).

<a id="turns-requirements"></a>
### Requirements

- <a id="u2"></a>U2. Take a turn: speak, and the agent answers out loud. Caller: a user.
- <a id="u3"></a>U3. Interrupt the agent mid-sentence. Caller: a user.
- <a id="f1"></a>F1. A provider (model or speech) is slow, down or rate-limited. Handled as failure modes of [U1](#u1) to [U5](#u5).

<a id="turns-assumptions"></a>
### Assumptions

- <a id="a5"></a>A5. Typing works as an alternative to speaking in every session.
- <a id="a6"></a>A6. An agent may stay silent on a turn; the user's words are still recorded.

<a id="turns-how"></a>
### How it works

<a id="pipeline"></a>
#### The pipeline

Every stage streams and starts on the first piece of input from the stage before:
- Audio flows in continuously.
- Speech-to-text returns rough text while the user speaks.
- The model's reply arrives word by word.
- voice streams the reply's text to text-to-speech, which returns audio in 20 ms packets and makes at most one phrase ahead of playback, so an interruption wastes little.
- Audio streams out over WebRTC.

Speech-to-text finishing and the end-of-turn check run at the same time, since one reads text and the other audio. Saving the turn and its timings happens off the critical path: the user's words are saved when the turn is confirmed, alongside the model call, and the agent's reply is saved as far as the user heard it.

Three rules keep it correct:
- One turn is in flight per session.
- A turn is saved before the next prompt is built.
- Nothing plays and nothing is saved until the turn is confirmed and the words sent to the model match the final text ([D30](#d30)).

<a id="d30"></a>
> #### D30. Start the model before the end of turn is confirmed
> - Decision: when the user pauses and speech-to-text returns final text, voice sends the turn to the model at once, while the end-of-turn check runs. If the check says the user hasn't finished, or they keep talking, the request is cancelled and resent with their full words ([D26](#d26)). Nothing plays and nothing is saved until the turn is confirmed. Speculative attempts per turn are capped.
> - Needed by: [U2](#u2); [NFR1 latency](#nfr1).
> - Assumes: most pauses that trigger the check are real ends of turn.
> - Why: the end-of-turn check and the rest of the pause overlap the model's first-word time instead of adding to it. LiveKit's agents do the same ("preemptive generation"), starting only the model early and holding speech until the turn is confirmed.
> - Trade-off: tokens spent on false starts, bounded by the cap.
> - Alternatives rejected: waiting for the check before calling the model (adds the check to every turn).
> - Status: deferred, after measuring it built: the end-of-turn check answered before the final text in every turn measured but one (on an M3 Pro, `.claude/skills/measure/check-speak.sh` with Sonnet 5.5 and Haiku 5.5: the check 0.33 to 0.42 s after speech ended, the final text 0.37 to 0.50 s), so there was nothing to overlap: it asked the model early once in 15 turns, 70 ms early, about 5 ms a turn on average, against holding and cancelling unconfirmed replies in conversation and speech. Spoken turns wait for both the check and the final text, then ask the model. Revisit when the end-of-turn check answers after the final text, as when it runs further away than speech-to-text, or speech-to-text gets faster; measure the share of turns where the check comes last first.
> - Enforced by: nothing while deferred; when built, a test that a cancelled speculative reply is never played or saved.

<a id="transport"></a>
#### Transport

<a id="d19"></a>
> #### D19. Audio over WebRTC, in the Go service
> - Decision: audio travels between browser and server over WebRTC, with the Go service as the other participant, using Pion. Whichever server answers a session's audio connection holds it. A session has at most one on a server, since a reply played on two would be heard twice: a newer one replaces the old, as reloading the page does, and the old one is told it was replaced, so its page says so and doesn't reconnect; otherwise two tabs would keep taking the session from each other. The server lets go of a connection that ends: hung up, lost, or stopped with the server. The session itself stays in the database, so a browser whose connection drops opens a new one with any server. Beyond that:
>   - A connection carries the agent's audio track, Opus packets sent as speech paces them and stamped as RTP asks (RFC 3550): sequence numbers count packets, timestamps count audio time with any silence between phrases and between replies, which speech counts from when the next packet would have gone out, so the browser takes a silence neither for lost packets nor for network delay, and the first packet after a silence is marked (RFC 3551); the user's microphone track, when the browser sends one; and the data channel the browser opens, which carries messages both ways in order.
>   - The page offers audio both ways at once, with no microphone track yet, and asks for the microphone with the browser's echo cancellation, noise suppression and automatic gain on; once the user allows it, the microphone joins the same transceiver with no new offer, so neither the connection nor typing waits on the prompt. A microphone refused, missing or never allowed leaves the transceiver without a track, and typing works as before; the user can mute a microphone given, which then sends silence, so nothing they say is heard; the microphone stops when the connection ends. Each Opus packet from the microphone is decoded in Go with pion/opus to 16 kHz mono PCM and handed to speech in stretches of 20 ms, whatever length the browser's packets are, on a channel holding a second of audio. Time is kept by the packets' numbers and timestamps: a duplicate, or a packet that comes after the one following it, is dropped; a packet numbered far behind is taken when the next follows it, since the browser has started its numbering again (RFC 3550); audio that never came counts as missing, at most 10 s before any one stretch, since a browser's timestamps can jump; and so does audio dropped when speech falls behind. What was dropped, and why, is logged when the track ends. The first packet of each Opus mode (SILK, hybrid, CELT) on a connection is logged, so what browsers send is known. A message larger than a turn can be (12 bytes for each character a turn may hold, as escaped JSON can take, plus 64 KiB) is refused: the limit is stated in the answer, so a browser won't send more, and one that does loses its data channel, since otherwise a message of up to a gigabyte would be held in memory.
>   - A server takes audio only on the addresses it is given (`VOICE_AUDIO_IPS`), by default its private and loopback ones, and refuses to start with an address none of its interfaces has, since nothing would listen there and every connection would be answered and then die at the ICE timeout. Each connection takes ports of its own, unless `VOICE_AUDIO_UDP_PORT` names one UDP port that all share on those addresses, told apart by their ICE credentials, so a container or a firewall opens a single port; a port the server can't take stops the start, and the port is let go once every connection has ended. `VOICE_AUDIO_ANNOUNCE_IPS` writes other addresses into the answer in place of the machine's own (a one-to-one NAT mapping), as a container whose port is published on the host's loopback needs, or a server behind a public address: each stands for every listening address of its family, and an answer keeps one candidate per address and port, so the listening address it stands for must be named. Who this stops is under [security and access](#platform-security).
>   - A server holds at most a set number of connections (`VOICE_MAX_AUDIO_CONNECTIONS`, 100 by default); past it, an offer for another session is refused with 503 and `Retry-After`, while one replacing a session's own still fits. A session holds one place while it has a connection here or offers being answered: offers for other sessions arriving together past the limit are refused at once rather than answered and then closed, a second tab's offer shares its session's place, and a reload keeps it even when the connection it replaces ends first. A session may have two offers being answered at once, as a reload racing a second tab makes, and a third gets 503, since each builds a connection before the limit counts it. The session is checked before its offer holds a place, so an offer for a session out of the caller's reach never takes one.
>   - Stopping ends every connection, each told `stopping` so its page reconnects to another server, and waits for WebRTC's own goroutines to finish, not just its sockets to close; an offer that arrives while the server stops, or is cut short by it, gets 503 so the browser sends it again.
> - Needed by: [U2](#u2); [U3](#u3) (interrupting needs echo removed while the microphone stays open).
> - Assumes: Chrome's echo canceller removes audio that arrives over a WebRTC connection ([A1](#a1)).
> - Why: the browser removes echo on the microphone by subtracting a copy of what is playing; audio arriving over WebRTC is always that copy. WebRTC also drops late audio instead of stalling, and stays inside Go with no extra server. 16 kHz is the rate speech detection and the end-of-turn check take, and holds all of speech; pion/opus decodes every mode in pure Go, within 47 dB of libopus, the reference decoder, on the recording the tests play, so the Go build keeps no C library. The pause between replies is counted like the silences inside one: uncounted, the browser took a reply as arriving seconds late, and on the page its first sound came about a second late in 4 of 31 turns, against none of 24 counted.
> - Trade-off: more setup than a WebSocket (the two sides exchange connection details first); some users need a relay (TURN) server at higher stages; no measured speed gain; a connection left on one server when its browser reconnects to another lasts until the browser's close reaches it or the connection times out.
> - Alternatives rejected: WebSocket, because echo cancellation can't be relied on with it: audio the page plays itself is not documented to be cancelled, so the agent would have to stop listening while speaking, which breaks interrupting; LiveKit (an extra media server; kept as the path if rooms or a managed setup are needed); decoding the microphone with libopus through cgo (the first C library in the build), or forwarding its Opus to the speech-to-text service (which takes PCM).
> - Status: accepted.
> - Enforced by: the transport swap point with its fake; tests with a Go peer that messages cross the data channel in order both ways, that audio packets reach the browser as sent, that a silence moves the next packet's timestamp and not its sequence number, speech's test that the pause since the reply before counts as silence, that a message over the limit never reaches speech, that nothing is sent once a connection has ended, that a recording of Opus packets in every mode comes out of the microphone track at 16 kHz within 40 dB of libopus's decoding, that packets of 10, 20, 40 and 60 ms and of several frames come out in 20 ms stretches, that lost packets count as the audio they held, that duplicates and late packets are dropped, that numbers and timestamps wrap around, that numbering started again is taken from its second packet, that a timestamp jump counts as 10 s at most, and that each mode is logged once; the page's tests that it asks for the microphone with those settings, offers audio both ways and opens the connection while the prompt waits unanswered, sends a microphone given on the same transceiver and none when refused or missing, sends silence while it is muted, one given later too, stops the microphone when the connection closes, drops or fails to open, and passes on a refusal, not a missing microphone, so the page says how to allow it; a test that audio speech is too slow to take is held to a second and the rest dropped but counted as missing; tests that a server holds one connection per session, tells a replaced one so, leaves a newer connection open when the replaced one's `DELETE` arrives late, lets go of one that ended and ends every one when it stops, returning once each turn in flight is saved; that a full server refuses another session with 503 but takes a reload, counting each session with offers being answered once, keeps a reloading session's place when its old connection ends, refuses a session's third offer at once, and keeps no place for a session out of the caller's reach, that an address the machine lacks stops the start, that connections sharing one port are told apart, that an announced address replaces the machine's own in the answer, that a port already taken stops the start and that closing the transport lets go of it, and that an offer cut short gets 503; waiting for WebRTC's own goroutines is checked in review, as no test catches the plain close reliably; a test in Chrome confirms the echo assumption once audio flows.

<a id="noticing-speech"></a>
#### Noticing speech

<a id="d20"></a>
> #### D20. Speech detection on the server
> - Decision: Silero VAD's model, version 5, runs in the Go service through govad, a port of it to pure Go, behind speech's speech-detection swap point. Each audio connection gets a detector of its own, which judges the user's audio in frames of 512 samples (32 ms at 16 kHz) and gives each the probability that it holds speech; the model's weights, 1.2 MB, are loaded once and shared by every connection.
>
>   Each audio connection's listener, made with its microphone's first audio, cuts the audio into those frames, keeps the last 8 s of it (256 KB), the most the end-of-turn check reads, and counts every length in the audio itself, so what it notices doesn't depend on when the audio arrived; audio the transport counted as missing is silence, since nothing of the user was heard in it, and whole frames of it are judged so without running the model:
>   - speech starts at the first frame whose probability reaches the threshold (`VOICE_SPEECH_THRESHOLD`, 0.5 unless set: the threshold Silero VAD's authors give the model);
>   - a pause is noticed once the user has been below it for `VOICE_PAUSE` (0.2 s unless set, about the gap people leave before taking their turn, so the end-of-turn check, not the wait, sets how soon the agent answers); quiet shorter than that is part of the speech, as between words;
>   - the maximum wait is noticed once the quiet lasts `VOICE_MAX_WAIT` (3 s unless set: about 3 in 10 pauses within speech run past a second, people stopping to think), and must be longer than the pause and at most 10 s, the most silence lost audio counts as;
>   - speech is noticed as held once its frames judged speech add up to `VOICE_INTERRUPT_SPEECH` within one utterance (0.2 s unless set, about the shortest word, so a single "Wait." holds and a click doesn't); quiet frames within the utterance don't count and don't start the count again, so two taps a few frames apart aren't held and a frame the detector loses inside a word doesn't reset it: long enough to cut in, which it does only while the agent's reply reaches the user ([D37](#d37)).
>
>   Each is noticed at the end of the frame that completes it, so up to 32 ms late. These are server-wide settings until agents carry speech settings, starting values tuned from measured sessions. Each connection takes the user's turns from what it notices ([U2](#u2)), and cuts in on the agent with speech held over it ([D37](#d37)); it logs what it notices and where in the audio, never the audio. `VOICE_SPEECH_DETECTION` chooses Silero (`silero`, the default) or the fake, which judges every frame silence for load tests.
> - Needed by: [U2](#u2); [U3](#u3).
> - Assumes: one network hop of extra reaction time is acceptable; per-stage timing checks it. govad keeps matching the original model; it has one maintainer, so a test compares the two.
> - Why: works the same for every client, including products that embed voice-guide; the server times it; only speech is sent to speech-to-text. In pure Go, the build keeps no C library and a spoken interrupt needs no network hop; govad judges a frame in 0.63 ms on an M1, its author's figure, and came within 0.000001 of the original on the test's recording. Silero's own wrapper also feeds the model the previous frame's last 64 samples, which govad can't take; against it govad differs by at most 0.697 (0.051 on average) and decides 5 of 166 frames differently at 0.5, all at the edges of speech, which is acceptable since an edge frame is 32 ms against a 0.2 s pause, 0.3 s of audio kept from before speech starts, and Smart Turn deciding the end of turn.
> - Trade-off: reacts one hop later than detection in the browser; one maintainer, so a fix to govad may be ours to make.
> - Alternatives rejected: detection in the browser (client-specific; added later only if interrupting feels sluggish in the timing); silero-vad-go with ONNX Runtime through cgo (a C library in the Go build, pinned to an old ONNX Runtime).
> - Status: accepted.
> - Enforced by: the speech-detection swap point with its scripted fake; a test that govad's probabilities on recorded speech are within 0.01 of those Silero v5's own ONNX model gave for the same frames, and that each connection's detector keeps its own state while several judge at once; depguard keeps govad in its adapter; table tests of the listener on a scripted detector: speech starting at the threshold, a pause noticed once and quiet shorter than it part of the speech, speech after a pause starting again, the maximum wait noticed once after the pause, speech held once an utterance's frames judged speech add up to the interrupt speech, counted across quiet within it, noticed once an utterance, and two speech frames with quiet between not, audio that never came judged as silence and whole frames of it never given to the detector, each event timed by the clock, audio in pieces of any size judged in whole frames in order, and only the last 8 s kept; tests that a connection logs what it notices in its microphone, and makes no listener until its microphone sends audio; voice refuses to start with a threshold outside 0 to 1 or not a number, a maximum wait over 10 s, a pause, wait or interrupt speech that isn't a positive duration, or a maximum wait no longer than the pause.

<a id="speech-to-text"></a>
#### Speech-to-text

<a id="d21"></a>
> #### D21. Moonshine in a small Python service with OpenAI's realtime transcription API
> - Decision: speech-to-text is Moonshine's English streaming models (MIT licence) in a small Python service that speaks the part of OpenAI's realtime transcription API voice uses, so another service with that API swaps in with no change to voice. Over a WebSocket at `/v1/realtime?intent=transcription`:
>   - a transcription session with no turn detection, so voice decides when each item ends ([D22](#d22));
>   - 24 kHz PCM16 appended as base64, then committed or cleared;
>   - rough text as deltas while the audio arrives, then one completed item per commit, with the lines Moonshine cuts at its own pauses joined into one;
>   - errors as error events.
>
>   Deltas only ever add text: the last word is held back while it may still change, and when Moonshine revises a word already sent, deltas pause until the rough text agrees with what was sent again, and the final text corrects the rest. Deltas come before the commit, carrying the ID the commit then names; OpenAI's documents don't say how a model that streams while the user speaks names the item before the commit, so voice's adapter takes an item ID it hasn't seen as the oldest utterance without one. The model size is a setting (`STT_MODEL`: tiny, small or medium; small by default); its files are fetched when the image is built, each checked against its SHA-256.
>
>   voice's adapter reaches the service at `VOICE_STT_URL` (by default where compose publishes it on this machine) and keeps one connection per audio connection, opened at its first speech and opened again after a failure, so resuming after a pause costs no new connection; each utterance is an item, its texts matched to it by the item's ID, since final texts may come out of order. It resamples speech's 16 kHz audio to the API's 24 kHz; any failure closes the connection, failing every utterance it carried that has no answer yet, each once, and discarding whatever else it sends; the next speech opens a fresh one, so a reconnect is paid only after a failure; opening, each send and each final text have their own deadline.
> - Needed by: [U2](#u2); [NFR1 latency](#nfr1); [NFR8 swappability](#nfr8).
> - Assumes: the vendor's speed figures hold on our hardware (small: text about 150ms after speech ends on an M3, 7.8% of words wrong; tiny 50ms and 12%, medium 260ms and 6.7%; vendor benchmark). `.claude/skills/measure/check-stt.sh` measured 0.12 to 0.26s from the end of a 4.9s sentence to its final text over every run, in a Linux container on an M3 Pro.
> - Why: fastest on CPU for live speech by those figures, and runs in a container. The heavy work runs in Moonshine's C++ core on ONNX Runtime, so Python adds one hop between containers, not slower recognition. OpenAI's API sends text while the user speaks, so the page can show it as they talk, and leaves the end of each item to voice.
> - Trade-off: a fourth language and a small service to maintain; Python handles many streams per process poorly, so scale means more containers; rough text can show a word Moonshine later corrects until the final text arrives; Moonshine's Japanese model is under a community licence.
> - Alternatives rejected:
>   - Speaches with Whisper: it sends no text until a commit (Whisper reads the whole buffer then) and needs a GPU for real time; it also speaks the realtime API's older beta session (turn detection at the top level, `pcm16`, server-side speech detection on by default), which this service refuses and voice's session update doesn't switch off, so it would need an adapter of its own; it stays the likely route to Japanese;
>   - whisper.cpp on the host: outside containers;
>   - WhisperLive;
>   - sherpa-onnx's Go API: would drop Python, but loses the swap by configuration, puts speech-to-text's load inside the Go service, and its Go Moonshine example decodes files, not live speech.
>
>   Cloud services such as Cartesia Ink-2 are candidates later, for price.
> - Status: accepted.
> - Enforced by: the speech-to-text swap point ([D2](#d2)); the service's tests, over a WebSocket with a scripted recogniser, of each message shape and rule above, and of Moonshine's lines joined into one item; the models' SHA-256 checked at build; voice's adapter tested against a recorded exchange with the service, with its resampling; `.claude/skills/measure/check-stt.sh` against the real model, by hand.

<a id="end-of-turn"></a>
#### End of turn

<a id="d22"></a>
> #### D22. End of turn: a pause, then Smart Turn, then a maximum wait
> - Decision: a short pause noticed by speech detection triggers Smart Turn v3.2, which judges from the audio whether the user has finished; if not, voice keeps listening up to a maximum wait. The pause and the maximum wait start at 0.2 s and 3 s, server-wide settings for now ([D20](#d20)) that become per-agent settings once agents carry speech settings, tuned by per-stage timing. Smart Turn runs in the speech-to-text service as an endpoint of its own, `POST /v1/turn/check`: it takes up to the last 8 s of 16 kHz audio as `audio/L16` and answers finished when the model's probability is over 0.5, the model's own threshold. Its input, Whisper's log-mel features, is computed in numpy, and the model runs on a thread of its own, off the event loop and never behind recognition. voice's adapter asks it at `VOICE_TURN_CHECK_URL` (by default where compose publishes the service on this machine), each check failing after a second, about seven times the slowest measured (79 to 142ms over every run), so the maximum wait decides when the check can't. One turn's speech, pauses within it included, runs to at most `VOICE_MAX_SPEECH` of audio counted from its first audio sent (a minute unless set; voice refuses to start with one no longer than the interrupt speech, or over the minute the speech-to-text service holds in one utterance, past which the service would drop it): there its open utterance ends, and the turn is taken with its final texts, without the check. The rest of their speech until they pause isn't heard; speaking again after a pause is a new turn, even before the capped turn's final texts are in, and is taken after it; every capped turn waits for its texts and is taken in turn.
> - Needed by: [U2](#u2), with its failure mode 12; [NFR1 latency](#nfr1).
> - Assumes: the model's published figures hold (BSD-2-Clause code and weights; about 8MB int8 ONNX; 12 to 65ms per check on CPU; 23 languages including Japanese). `.claude/skills/measure/check-stt.sh` measured 79 to 142ms per check over HTTP over every run, in a Linux container on an M3 Pro. With spoken turns, `.claude/skills/measure/check-speak.sh` measured the check's answer 0.34 to 0.41 s after the speech ended (0.22 s of it the pause, counted in whole frames) and the final text 0.41 to 0.50 s after, so the final text, not the check, sets when a turn is taken; it took a sentence with a colon as two turns, at the colon's pause, which talking on after the end of turn ([D26](#d26)) now makes one.
> - Why: a silence timer alone either cuts users off (short) or feels slow (long); judging from audio pairs with any speech-to-text. Outside the realtime API, the check stays when a speech-to-text service with that API is swapped in; in Python, it runs on ONNX Runtime's own package, where the Go service would need its first C library. A minute of speech with no end of turn is more often a long answer than misuse, so it is taken rather than refused: its words reach the agent with no step of the user's, and the reply can be cut in on; speech that never pauses, a television left on say, costs at most a model call a minute, and its words never outgrow what a turn may hold (a minute of speech is about 900 characters). A minute is a round guess matching the most the speech-to-text service holds in one utterance, so none it is sent overflows.
> - Trade-off: one more model in the path; settings to tune per agent; a user who talks on past a minute is answered mid-thought, and the rest of their speech until they pause is lost.
> - Alternatives rejected: a silence timer alone; LiveKit's turn detector (about 400MB of memory, its own licence, its best version free only on LiveKit Cloud); Smart Turn in the Go service now (ONNX Runtime as a C library and Whisper's features ported, for a hop between local containers; moved if timing shows the hop costs); refusing a turn whose speech passes the cap, its words given back for the box (a long answer would need sending by hand).
> - Status: accepted; the pause and maximum wait values are tuned from the first measured sessions, and the cap on a turn's speech against real answers' lengths once sessions are timed.
> - Enforced by: the speech-to-text service's tests of the check (the threshold, L16's byte order, bodies refused, the model off the event loop) and of its features against the reference inference's output; voice's adapter tested against the service's recorded answers, with its deadline; `.claude/skills/measure/check-stt.sh` with a finished and an unfinished sentence, by hand; the pause and the maximum wait by the listener's tests ([D20](#d20)); tests that a turn is taken once the check says finished and its final text is in, in either order, and by the maximum wait when the check says not finished, fails, is late or is down, its stage times saying so; that a turn whose speech reaches the cap is taken with what was heard, exactly the cap's audio sent, pauses within it counted, and no check asked, and the rest of their speech until they pause unheard; that speech after it, its final text late, is a turn of its own, taken after it; voice refuses to start with a cap that isn't a positive duration, is no longer than the interrupt speech or is over a minute; `.claude/skills/measure/check-speak.sh`, spoken turns end to end against the real services, by hand.

<a id="text-to-speech"></a>
#### Text-to-speech

<a id="d23"></a>
> #### D23. Kokoro on the server, short first phrase
> - Decision: text-to-speech is Kokoro on the server's CPU, in a container (Kokoro-FastAPI). The swap point:
>   - takes the reply's text as it streams and the voice and speed to speak in, per call, since each agent and each session chooses its own;
>   - yields Opus packets of 20 ms, each marked with its phrase: its number, its text, where it ends in the reply's text, when its text was complete, and how many packets it has, where the implementation knows by the first;
>   - keeps reading the text while it speaks and after a failure, until the text ends;
>   - ends with one error on a failure; how much text to wait for is each implementation's choice.
>
>   Kokoro's adapter cuts the text into phrases and asks for each as streamed Ogg Opus:
>   - The first phrase ends at the first sentence end, however short, or at the first other punctuation once it holds four words: a comma, semicolon, colon or dash (not one between digits, as in a range of years). Ending it also before "when", "because" or "if", unless the word before binds to it ("even if", "as if", "only when"), is an option of the adapter, off until measured. Until four words have been cut, the phrases after the first follow the same rule. A short sentence holds a letter, so an ellipsis or a list's number never plays alone, and a period inside a word ("U.S.", "e.g.") or after a title (Mr, Mrs, Ms, Dr, Prof, St) ends no sentence. A sentence end at the very end of the text so far counts at once, since the model's text arrives in bursts and one can end at "Okay.", unless the period follows a digit or a single letter, which may go on ("2.5", "e.g.").
>   - Later phrases are whole sentences of at least four words; no phrase is cut inside a clause.
>   - It asks for one phrase at a time, the next once the one before is made, so the next is made while the one before plays and never more than one ahead.
>   - It reads each phrase's audio whole before its first packet goes on, so every packet says how many its phrase has; a phrase over 3,000 packets (60s of audio) or 4 MiB fails as a broken stream, and so does one whose rest doesn't follow its first packet within the same set time as its first audio.
>   - It unwraps the Ogg pages into packets itself.
>   - A phrase whose first audio hasn't arrived within a set time (`VOICE_TTS_FIRST_AUDIO`, 5s by default) fails, and so does an answer with no audio packet.
>   - Its errors carry Kokoro's status, never Kokoro's reason.
>
>   `VOICE_TTS` chooses Kokoro (at `VOICE_TTS_URL`, by default where compose publishes it on this machine) or the fake, which speaks silence at the pace of speech for load tests. Every reply is spoken in its session's voice and at its speed, chosen when the session starts: the start's, else the agent file's, else the service's (`VOICE_TTS_VOICE`, `af_heart` by default, the voice Kokoro's times were measured in, at Kokoro's own pace); speech reads them once, with the session, when its audio connection opens. `GET /v1/voices` lists the voices the text-to-speech service offers, from Kokoro-FastAPI's `/v1/audio/voices`, which the MLX server answers in the same shape; a start naming a voice is checked against that list, which is waited for 2 s at most (a round guess, to be measured under load), and a start naming none never asks it. The list leaves out any name a start would refuse, so a listed voice can always be named; an answer with no list, or a null one, is as good as none and is unavailable, where an empty list is an answer. Speech's own core never asks for the list: the Kokoro adapter has the method, and `cmd/voice` hands it to conversation as its port. Both engines take the speed, Kokoro-FastAPI as its `speed` field and the MLX server as `generate`'s, and the MLX server refuses one outside 0.5 to 2 as voice does. Speech plays the packets in real time, one every 20 ms counted from the first, and starts the count again after a gap rather than catching up in a burst; the reply's text reaches the page as it arrives, never waiting on the audio. Kokoro can also run natively on a Mac with Apple silicon (`KOKORO=mlx`): the same model and voice through Apple's MLX, in a small server of ours with the same API, standing in for a dedicated text-to-speech server with a GPU while the container stays the default anyone can run; its times are named by machine. The container's own server on the Apple GPU, through PyTorch, was tried first and dropped for MLX: each new length of phrase cost it extra time the first time, and its memory kept growing, as measured below. Measured with `.claude/skills/measure/check-tts.sh` on an Apple M3 Pro (five replies, three warm runs each, 2026-11-19), a reply's first audio took 0.37 to 0.80 s through PyTorch on the Apple GPU (about 0.57 s on average) and 0.84 to 2.37 s in the container on the CPU (about 1.2 s), the same release and lockfile on both. Those were the same five replies made again and again: PyTorch on the Apple GPU prepares its work once for each new size of input, so a phrase of a length it hasn't made yet took 0.5 to 1.8 s more (0.13 s for a three-word phrase made before, 1.84 s for a new one-word phrase), its memory grew with each (5.1 GB after about 250 different phrases), and warming it up with phrases of every length from 2 to 40 words left new ones at 0.27 to 0.79 s. In spoken turns on the Claude API, Kokoro's share was 0.5 to 0.95 s through PyTorch against about 0.75 to 1.2 s in the container. MLX has no such cost: on the same machine and replies, first audio took 0.31 to 0.71 s (about 0.50 s), the same on the first run as on repeats; a phrase made the first time took 0.13 s for a few words, 0.3 s for 13 and 0.68 s for 39, and memory stayed at about 0.66 GB over 150 different phrases. In voice, with the fake model, a first phrase's being ready to its first audio going out took 0.23 to 0.25 s. Kokoro leaves about 0.3 s of silence before a phrase's sound and nearly 0.5 s after it. The container's server trims it, keeping 50 ms before the sound and, after it, a pause of 410 ms to the next phrase's sound after a full stop or a question (0.9 of that after an exclamation, 0.8 after a comma); ours on MLX trims it the same way, so both voices pause alike. Measured with `.claude/skills/measure/check-page.sh` on an Apple M3 Pro, a reply on MLX started sounding 0.32 to 0.34 s after its first packet reached the page untrimmed, against 0.07 to 0.09 s trimmed, and in typed turns on Sonnet 5.5 through the Claude API the pauses inside a reply ran to 0.83 s untrimmed, against 0.51 s trimmed.
> - Needed by: [U2](#u2); [U3](#u3) (its audio reaches the browser over WebRTC, so echo cancellation covers it); [NFR8 swappability](#nfr8).
> - Assumes: CPU speed is enough for one user; per-stage timing shows when it is not.
> - Why:
>   - Free and natural-sounding; on the server its audio goes over WebRTC.
>   - Opus straight from Kokoro leaves voice with no audio encoder, and 20 ms packets go out on the audio track as they are, paced by counting them.
>   - A phrase's end in the reply and its packet count tell how much of the reply was heard without searching for it ([D36](#d36)); the time its text was complete gives the first speakable phrase's stage time. Reading a phrase whole costs only the reading, since Kokoro sends it all at once (first audio 0.86 to 1.67s over the five questions, against 0.86 to 1.76s streaming each packet on).
>   - Kokoro sends a phrase only once all of it is made, and its time is mostly fixed (0.48s for one word, 0.58s for four, 0.75s for eight, on the development machine), so a short first phrase starts the speech sooner, and four words keep it from sounding clipped: each phrase is spoken with its own rise and fall and a pause after it, so "So," or "For example:" alone would break its sentence in two. A whole sentence is the exception, since a speaker pauses after one anyway. The model's text arrives in bursts: on Sonnet 5.5 through the Claude API, the first few words came 0.8 to 0.9 s after the press and the next burst 0.45 to 0.9 s later (`.claude/skills/measure/check-page.sh`, Apple M3 Pro), so a reply opening with a short sentence ("Okay.", "I see.") starts sounding from the first burst, at 1.03 to 1.13 s after the press against 1.67 to 1.97 s for replies opening the same way when they waited for four words; and once a short first sentence is cut, the next phrase ends at its first break too, so it is ready before the short one has played, where a whole sentence left a pause of up to a second after it. "That", "after", "before", "and", "but" and "so" don't cut, since they are as often a determiner, a preposition or a joiner of words ("walk me through that decision", "after that", "your team and your manager"), and a comma before a joining clause cuts anyway. Word cuts are off by default because grammar comes first: cut at "when", first audio came at 0.76s, against about 1.5s for a 15-word sentence spoken whole, a gain to be measured on real replies (punctuation only, these three words, and three more: "although", "unless", "until") before it is turned on.
>   - Asked for together, phrases slow each other (a six-word phrase took 0.77s alone, 2.0s beside the next), and one phrase ahead keeps what an interruption throws away small.
>   - Kokoro puts a second of audio, 50 packets, in each Ogg page, and Pion's Ogg reader returns whole pages, losing where packets end.
>   - The deadline lets a user left in silence learn soon that something failed (5s leaves room for sentences longer than the 16 words Kokoro made in 1.33s); an answer with no audio would otherwise pass as heard.
>   - A failure must not stop the reply reaching the page as text.
>   - Kokoro's reasons can quote the text it was given, which holds what the user said, and errors reach logs.
> - Trade-off: server CPU per conversation; on CPU its first audio is close to the model's first word, so it may be the slowest stage until swapped (0.7 to 1.5s from the model's first word to first audio in four runs of five interviewer questions with `.claude/skills/measure/check-tts.sh`, the longest for a question with no comma; no pause between phrases); with word cuts off, a first sentence with no punctuation is spoken whole (that question took 0.71 to 0.77s when "that" still cut it); each phrase waits for the one before; what was heard within the phrase playing is estimated from the share of its packets played, not known to the word ([D36](#d36)); a vendor that gives only raw audio needs an Opus encoder in its adapter; a short first sentence is heard sooner, so a user who goes on talking once it has sounded cuts in on the reply rather than having their words joined into one turn.
> - Alternatives rejected: Piper (sounds synthetic; licence change unchecked); raw audio through the swap point with one encoder in voice (a C library or a young encoder from the start); word timings now (nothing uses them before interrupting); the voice fixed when the adapter is made (agents choose their voice and are published at runtime); Pion's Ogg reader (above). Candidates behind the swap point later: Kokoro in the browser (free at any scale, but then echo handling), Kokoro on a GPU, Cartesia Sonic, ElevenLabs Flash.
> - Status: accepted.
> - Enforced by: the text-to-speech swap point ([D2](#d2)) with its fake, which can be told to fail; tests of the cutting rules, with word cuts off and on, and each phrase's end, of one phrase made at a time and at most one ahead, of the text still read after a failure, of the deadline, that every packet carries its phrase's packet count, that a phrase over either limit fails with none of it played, and that one whose rest stalls fails, and that only 20 ms Opus packets with at least one packet of audio pass and errors carry no reason; that each phrase is asked for in the voice and at the speed given, and with none for a speed of 0; that the voices read back from Kokoro's route and fail on a refusal, an answer that isn't a list, one with no list or a null one, and a service not listening, while an empty list is answered; that a listed name a start would refuse is left out; that a list that stalls is given up on at its deadline with 503 and nothing saved; that `cmd/voice` builds conversation with the list, so `GET /v1/voices` answers 200; that publishing refuses an agent naming a voice the service lacks, and publishes with a warning when the service can't be asked; that every reply of a session is spoken in its voice and speed, the service's when the agent and the start name none, the agent's, and the start's over the agent's; that a start names a voice and speed in range or is refused before the list is asked, a voice not listed is refused and one the list can't be read for is refused with 503, a start naming none starting with the list down, and a repeat keeping the first start's voice, the reply naming the voice and speed in force and a repeat with the list down answered without asking it (also against PostgreSQL, the speed exactly); tests that the MLX server's voices are the model's own, sorted, by name only, and its speed a number from 0.5 to 2; tests that the MLX server's trim keeps 50 ms before a phrase's sound and its pause after, shorter after an exclamation or a comma, and leaves a short tail or a phrase with no sound as it is; `.claude/skills/measure/check-tts.sh` against the real Kokoro.

<a id="the-model"></a>
#### The model

<a id="d24"></a>
> #### D24. A layered, cached prompt
> - Decision: the prompt is built in layers, in this order:
>   - the agent layer: persona, rules, rubric; the same for every user of the agent version; cached;
>   - the session context the product sent and the user context chosen for this session ([D35](#d35)), cached for the session;
>   - the conversation: append-only; changes such as a phase move arrive as messages, never as edits to the layers above; kept under a size limit set by measurement;
>   - the new message: the newest screen input block of each name ([D35](#d35)) and the user's new words.
>
>   One function renders the prompt from what the session recorded, so every turn and every instance produces the same bytes:
>   - the agent layer: the version's persona as written;
>   - the session's layer: a fixed line saying the product sent what follows, then each session context block in the order sent, as `<name>`, the content, `</name>`; then a second fixed line and each context item the session loaded, read back by ID in the order loaded, tagged with its kind; an item replaced since stays, and the change reaches the session as a message in the conversation ([D27](#d27)); left out when the session has neither;
>   - a saved user's message: a fixed note that they cut the reply before it short, if they did (another when they cut in before it said anything), then a fixed line for each screen input block the turn carried, "(The user's <name> changed here.)", in place of its content, then the words, each part after a blank line; the agent's reply exactly as saved, left out when a lost turn has none; a reply the user cut short only as far as they heard it, with a dash for the break, left out when they heard none of it ([D36](#d36));
>   - the new message: the note, then the newest block of each name the session has had, taken from the saved turns or from this turn, whose own block wins, tagged the same way, in the order the names first came, then the words; the request also carries how the message will read once saved, for a provider that keeps the conversation between turns;
>   - nothing that varies by turn, such as a time, a count or a map's order, in the stable layers.
>
>   Each turn the cache point moves to the last saved message: the turn reads the conversation before the last saved exchange from cache, writes that exchange to it, so the next turn reads every earlier turn from cache, and the new message sits after the cache point: a board that changes every turn is read fresh once, as the newest block, and never changes the cached conversation, where each change stands as one fixed line on the turn that carried it. The exception is the first turn after the model service rebuilds a session process ([D25](#d25)). Reference material lives outside the prompt and is looked up when needed. Conversation turns use a model setting without up-front thinking.
> - Needed by: [U2](#u2); [NFR1 latency](#nfr1).
> - Assumes: prompt caching works as published: any change to a byte invalidates everything after it, so stable layers come first.
> - Why: every uncached input token adds to the time to first word on every call; Anthropic's launch figures (2024) show caching cutting time to first token by 31% to 79% depending on the cached share. Long inputs also make answers less reliable (Chroma, "Context Rot", 2025).
> - Trade-off: changes mid-session must be expressed as messages; reference material needs a lookup step; the newest screen input block of each name is read fresh on every turn, whether or not it changed; each turn's screen input has a size limit, but the number of names a session sends has none yet, so what is read fresh is bounded only per name.
> - Alternatives rejected: rebuilding the whole prompt each turn (breaks the cache); loading all reference material into the prompt (slower and less reliable); replaying every saved screen input block whole (a board change would break the cache from the turn that carried it, and every earlier board would be read again on every turn); a table of the session's current screen (a second record to keep in step, where the saved turns already hold it).
> - Status: accepted.
> - Enforced by: tests that the stable layers are byte-identical on every turn of a session and when built twice from the same records, that the session's layer is left out when empty, and that a saved reply comes back byte for byte in the conversation, and a cut reply as far as it was heard, the note opening the next user message; that a saved turn's screen input comes back as its marker, exactly as its own request said it would be saved, the newest block of each name going whole with the new words, a new turn's own block winning and two names keeping their first order, that a board changing every turn leaves the conversation before it byte for byte, and that the same stored turns give the same prompt; tests that reading the loaded items back keeps their order and refuses another user's or workspace's. A test that a mid-session change leaves the stable layers byte-identical comes when context changes reach a running session.

<a id="d25"></a>
> #### D25. Model access behind a provider-neutral swap point
> - Decision: voice sends a layered prompt (stable prefix marked cacheable, conversation, new message) and receives streamed text with usage and timing. Which model answers is the agent version's own when its file names one, else one setting of the service, and is sent with every request and recorded with every turn; the setting is the default, so changing it moves every agent that names none. Provider features (prompt caching, message threads, mid-conversation system messages) live in each adapter. During development the adapter is a small TypeScript service, the model service, with three providers chosen by its settings, each named for whose model it reaches and how that is paid for:
>   - `claude-api`: Claude through the provider's Messages API, streamed, billed per token to an API key;
>   - `claude-subscription`: Claude on a personal subscription, through the Claude Agent SDK;
>   - `fake`: replies with set text at a set pace, for load tests.
>
>   voice calls it over HTTP, one request per reply (the model, the session, the stable layers, the conversation, the new words, and, when screen input makes them differ, how they will read once saved and with only the screen input the turn carried), answered by a stream of JSON events, one per line: text as it arrives, then usage and timing, or an error naming its kind. The kinds (rate limited, quota, overloaded, unavailable, auth, bad request, too large, unknown; quota is a subscription usage limit, which clears only when it resets; too large is a conversation that has outgrown the request size limit, which no retry fixes until the conversation is shortened) let voice tell a failure a brief retry may clear from one no retry fixes ([U2](#u2) failure mode 5); the provider's own message is never passed on or logged, since it may echo what the user said. Each reply has a deadline, after which it ends as unavailable; at most a set number of replies run at once, and more are refused for voice to retry; a request's size is capped; all three are settings, refused at startup when unusable ([F1](#f1), [U2](#u2) failure mode 12). The model service has no login of its own, so it listens only on this machine unless its address is set: otherwise anyone on the same network, a shared Wi-Fi say, could send it prompts and spend the Claude login it runs on. A container sets all interfaces, reachable then only inside its own network. Every reply ends with exactly one final event, and nothing a provider sends after its deadline reaches voice. voice's client reads the events line by line, each line capped in size, within the turn's deadline, and closes the connection when the turn is cancelled, which stops the provider; a refusal at the cap reaches voice as overloaded, any other refused request as bad request, a stream that ends without its final event, even midway through a line, as unavailable, and an error outside the closed set of kinds as unknown; a final event whose report doesn't read, or lacks the provider, the model or the total time, still ends the reply whole, without the report, and the turn records the model voice asked for. Claude Code's and the API SDK's own retries are off, so voice alone decides when to retry. Other providers come later.
>
>   A request outside any session, such as a session's summary ([D39](#d39)), carries no session: the model service keeps nothing for it, and runs one such request at a time within its cap on replies, which may be no fewer than two, so live turns keep the other places. `claude-subscription` keeps one Claude Code process per session between turns, its session process, so the next turn skips the process start and sends only the new words. The process records the new words as voice will save them, screen input as its markers, so the next turn's conversation matches; a kept process is sent only the screen input the new turn carried, with the words, since it holds every earlier block already, and a fresh one gets the whole prompt. The process still holds every block it was shown, which only a fresh process drops. The request's two extra forms of the new words are left out when they read the same; a model service older than voice ignores them and replaces the session process every turn, so the model service is deployed before voice. The process is a disposable cache ([D14](#d14)): voice still sends the whole conversation, and when it differs from what the process has seen (a reply cut short, merged words, another model or stable layers), or the process has died before saying a word or reporting a failure, a fresh process gets the whole conversation as a transcript; only that turn is slower. A process is closed when its reply fails or is cancelled; when its caller cancels it, a fresh one is started for the session at once, since the session is most often asked again within a second (the user talking on, a reply cut), and that ask takes it with the whole conversation, as any fresh process does, without waiting for Claude Code to start. None is started in place of another session's kept process, after a reply's deadline, or once the service stops, which closes the provider before its connections. voice's own reply deadline, shorter than the service's, arrives as a plain cancel and starts one too: it takes only a free place and is closed after the idle time like any kept process, so it never costs another session its process. At most a set number are kept between turns, the least recently used closed first, each closed after a set idle time; a running reply holds its process outside that count, so at most that number plus the replies at once are alive, plus closed ones still exiting (about 2.7s each, since Claude Code stops on its own once told). Both are settings, checked at startup like the limits above. Claude Code's own compaction is off: it would summarise the conversation inside a turn, making the turn wait, and leave the process holding a summary where voice expects the conversation word for word; shortening the conversation is voice's. Claude Code gets only the environment variables it needs to find the login and run, named one by one, so the service's own secrets, such as the signing key and the database's address, never reach it; an API key is never passed, since Claude Code would bill the key instead of the subscription.
>
>   `claude-api` keeps nothing between replies, so any copy of the service answers any turn: each request carries the agent's layer and the session's layers as system blocks, the conversation as the two sides in turn, and the newest words last; voice always starts the conversation with the user's side (an agent's opening with its cue), and a conversation that would start with the agent's, which voice doesn't send, is opened by one fixed line, since the API takes the user's first. A request with blank new words is refused as a bad request, since it would end on the agent's side. The API's prompt cache holds the agent's layer, shared by every session of its version, the session's layers and the conversation so far, with a cache point after each (three of the API's four), the conversation's on the last saved message: a turn reads what came before the last saved exchange from cache, writes that exchange to it and reads the new message, the newest screen input and the words, fresh, and a board that changes leaves the cache whole; the opening, with nothing saved before it, caches its layers only, and an ask outside any session caches nothing past its layers, since it is asked once. Thinking is turned off each model's own way (Sonnet 5.5 refuses "disabled" and takes "between tools"; Haiku 5.5 takes "disabled"), with the least effort and no sampling settings; a model the provider has no rule for is refused as a bad request rather than left thinking. A reply's length is capped by a setting, 1,024 tokens unless set and at most 8,192, and one stopped there, or refused by the model, ends with what was said, a refusal logged with its category. The API key is read by the model service for this provider alone, which refuses to start without one, naming the other two providers; the scripts pass it to the model service alone, so voice and everything else they start runs without it. The SDK calls the API's own address whatever the environment names, so a proxy set for Claude Code never receives the key or a conversation; its timeout is the reply's deadline, and its own logs and traces are off; out of credit is an auth failure, which no retry clears.
> - Needed by: [U2](#u2); [U4](#u4); [NFR8 swappability](#nfr8); [D28](#d28).
> - Assumes: the subscription route works for one person under current terms (confirmed when the service was first built); it is for personal use only.
> - Why: free during development; any model can replace it behind the same interface.
> - Trade-off: on the subscription route, measured with `.claude/skills/measure/check-model.sh` (Sonnet 5.5, three turns, two runs each), the second and third turns took 0.96 to 1.21s to the model's first word with a fresh process for each reply, and 0.48 to 0.62s with session processes kept; the first turn starts a process either way (0.88 to 1.15s, once 3.8s on the first run after installing). The process itself starts in about 0.2s. Asked again 0.4 s after a reply was cancelled, with the same conversation, the first word came a median 0.55 s (Haiku 5.5) and 0.64 s (Sonnet 5.5) later than on a kept process when a process started with that ask, and 0.15 and 0.02 s later with the process started at the cancel (seven rounds each, against the model service directly; single rounds varied by seconds with the API's own speed). A session process holds about 250 MB, and a closed one takes about 2.7s to exit. Claude Code may still think before replying although the provider asks it not to, and it adds the account's email to every prompt, so personal data reaches the model in every session: acceptable only on a maintainer's own machine. Per-stage timing records which adapter served each turn; the subscription route must be replaced before anyone else uses voice-guide. The API route adds nothing to the prompt and bills every turn: one reading a 2,000-token prompt from cache, writing its newest 250 tokens there and answering in 100 costs about a fifth of a cent on Sonnet 5.5 ($2 and $10 per million tokens in and out, $0.10 read from cache) and a hundredth of a cent on Haiku 5.5 ($0.10 and $0.50). Measured with `.claude/skills/measure/compare-models.sh` (ten typed turns each, an Apple M3 Pro, 2026-11-19), the first word came a median 0.66 s after the ask on Sonnet 5.5 (90th percentile 0.71 s) and 0.52 s on Haiku 5.5 (0.56 s), against 1.63 s (2.99 s) and 0.84 s (1.33 s) on the subscription route the same night, with 88% and 95% of the prompt read from cache.
> - Alternatives rejected: the API from the start (billed per token during development); the Go SDK in the Go service (no subscription route in Go).
> - Status: accepted; the API route is the default, and the subscription route is only for a maintainer's own machine.
> - Enforced by: depguard lists (no provider package in conversation's core, and only the standard library in conversation's client); tests of the client against a reply recorded from the model service, for each event, refusal, cut stream and malformed line; tests that a turn and a summary send the agent version's model, else the service's setting; the model service's tests, which check that a reply uses the model it was sent and ends with usage and timing, and that a failure ends with its kind and never its message; tests for each limit and that the address defaults to this machine; tests that a session process is reused only for exactly the conversation it has seen, including a turn whose board the next conversation holds as its marker, and that a kept process is sent only the screen input its turn carried, that a cancelled reply starts one for the session's next ask, taken by it only on the same model and stable layers, and none in place of another session's, after a deadline or once the provider closes, closed before the connections; a test that Claude Code gets only the environment variables it needs; tests that the API provider sends the layers and the conversation with their cache points, the conversation's on the last saved message and none on the new message, so two requests differing only in the board share all before it, turns thinking off for each model it knows and refuses any other before calling, ends a stream cut midway as unavailable and a cancelled one with nothing more, maps each failure to its kind without its message, refuses blank new words, and builds its SDK client on the API's own address whatever the environment names, with retries, logs and traces off and the reply's deadline; a test that the API key is read for that provider alone and its absence stops startup.

<a id="d26"></a>
> #### D26. Talking on after the end of turn: merge, until the agent speaks
> - Decision: a spoken turn stays in flight after its reply ends, until the reply's first packet is heard, by the same estimate as the part of a reply heard ([D36](#d36)), and as long again as speech can take to be noticed (a detection frame and a 20 ms microphone packet), or until the reply stops with none, but never past its reply's deadline, so it still ends within the turn deadline; then it is saved as it ended. Speech that begins before then is the user talking on. The reply stops at once, no further packet going out, and the page is told (`talking_on`), so it takes the reply's text away. The turn is taken back: removed while still in flight, in one write, as if never taken, so no prompt holds a reply nobody heard; since one turn is in flight per session, nothing else can have started meanwhile, and no finished turn is ever removed. What the user goes on to say joins the turn's words, taken in its place under the same request ID once they finish. From the first packet heard on, speech over the reply is an interrupt ([U3](#u3), [D37](#d37)), so the two meet with no gap. A turn whose speech ran to the most one may hold isn't reopened. A turn that can't be taken back stays in flight, and speech remembers it: all the words after it are refused as busy whenever they come, past the turn deadline too, where they would otherwise cut it off under the same request ID and be refused as a repeat, and go back to the page; the turn is saved lost once past the turn deadline, as a turn a crash cut off. Talking on after a typed turn isn't merged: typed words aren't heard, so speech after them is a turn of its own.
> - Needed by: [U2](#u2).
> - Assumes: most early cut-offs are caught before the first audio.
> - Why: the user's whole thought reaches the model, and nothing the agent said is lost. Holding the turn in flight until it is heard, rather than saving it at once and taking a finished turn back later, keeps every turn the next prompt is built on in place: a finished turn removed could have its number taken by a turn whose prompt lacked it.
> - Trade-off: each merge cancels a model request that is partly billed, and the next ask on the subscription route waits on a session process started at the cancel ([D25](#d25)); a spoken turn's end is saved up to its first audio's trip later, and the page told it ended only then; until the merged turn is taken, the words said before talking on are held only by the server hearing them, as any words still being heard are. `.claude/skills/measure/check-speak.sh` on an M3 Pro: the sentence with a colon, taken at the colon, was talked on in time and answered as one turn, its first audio sent 2.13 s (Sonnet 5.5), 2.80 s (Haiku 5.5) and 2.26 s (the fake model) after the whole sentence ended; before, its first half was answered, that reply sounding while the user still spoke, and the rest was a turn of its own, answered 3.18 and 2.81 s after it ended.
> - Alternatives rejected: answering the first part and treating the rest as a new turn (the agent answers half a thought); taking back a turn already saved as ended (a turn typed meanwhile could take its number, its prompt lacking the turn; and a failed removal leaves a reply nobody heard in later prompts); rewriting the words of the turn in flight while the user speaks on (it would sit in flight, past the turn deadline for a long answer).
> - Status: accepted.
> - Enforced by: speech's tests that talking on before the reply is heard takes the turn back, plays none of the first reply and takes all the words as one turn under the first's request ID and number, that speech once the reply is heard interrupts it instead, that talking on ends exactly where speech over the agent begins, that talking on, settling the turn and the reply's first packet exclude each other, that speech begun just before the reply is heard, noticed a frame and a packet later, still talks on, that a reply that never sounds is settled by its turn's deadline, that a capped turn isn't reopened, and that a turn that can't be taken back gives all the words back and saves nothing more, also against PostgreSQL past the turn deadline; conversation's tests that a held turn stays in flight until settled, other turns refused meanwhile, and is settled within the turn deadline, that a turn taken back is removed and all the words take its place, that only a turn still in flight can be taken back, and that a turn typed after a take-back comes before all the words, never in place of a finished turn; the store's test that only a turn in flight in its own workspace can be removed; the page's test that the reply goes and one reply follows all the words.

<a id="turns-use-cases"></a>
### Use cases

#### Take a turn ([U2](#u2))

Typed turns, as built so far: voice checks the words (some, unless screen input is sent; no NUL character; at most `VOICE_WORDS_MAX_CHARS`, 4,000 characters unless set) and reads the session within the caller's workspace and user, refusing alike one that is missing or another workspace's or user's, before anything is saved or sent. A request ID that already took a finished turn in the session is refused before the model is asked, so a repeat costs no model call. It builds the prompt from what the session recorded and its earlier turns ([D24](#d24)) and asks the model, saving the user's words meanwhile as the session's next turn. The save is refused while another turn is in flight, once a turn the prompt doesn't hold has finished, and for a request ID the session already took a turn with (a repeat that raced the first). A repeat is acknowledged as taken (`/v1/problems/turn-repeated` over HTTP, `refused` as `repeated` on the data channel) and never answered again: its reply is not sent or spoken a second time, so its text shows at most once. No text reaches the user before the words are saved, and a refused save stops the model. The reply is saved exactly as the model sent it, so the next turn's conversation holds the model's own words; a reply the model doesn't finish, one whose caller goes away, or one still running when voice stops, is saved as lost with the text sent so far. A lost turn with no text adds only the user's message to later conversations.

Over HTTP, a typed turn is a `POST` of the caller's request ID, the words and any screen input, or, for the agent's opening, `opening` set and no words ([D18](#d18)), answered by a stream of server-sent events, each sent the moment it is ready and marked for a proxy in front not to buffer: `text` as the reply arrives, then `done` with the turn's ID and number. A reply that fails ends with `lost`, even before its first text, since the turn is saved lost; so does a turn whose end couldn't be saved once its text had started, naming no turn, since only a turn saved as ended is named. A turn refused before any event is a problem with its status:
- 404 alike for every session out of the caller's reach, another session's ticket included;
- 409 while another turn is in flight, including a repeat sent while the first is still running, 409 of type `/v1/problems/turn-repeated` for a request ID that already took a finished turn, and 409 of type `/v1/problems/opening-refused` for an opening the session doesn't take;
- 400 for a malformed request, words that can't be used, words with an opening, or screen input that can't be used;
- 413 over a limit.

Spoken turns, as built so far, without the speculative start ([D30](#d30)), on each audio connection:
- At the user's first speech, the connection opens its speech-to-text stream, which lasts as long as the connection ([D21](#d21)). Each utterance's audio goes to it from 0.3 s before the speech began, since speech detection judges a word's quiet start as silence, then as it arrives; the rough text reaches the page as `hearing` ([D11](#d11)).
- At each pause, the utterance ends, which asks for its final text, and at the same time the end-of-turn check is asked about the last 8 s of audio.
- Once the check says the user has finished and every utterance's final text is in, in either order, the turn is taken with the final texts joined, under a request ID speech makes, through the same path as a typed turn on the data channel, at most two waiting. Speech that began while the agent's reply reached the user has cut in on it by then, once held, or been passed over ([D37](#d37)).
- When the check says the user hasn't finished, speech keeps listening: speaking again continues the same turn, and the check's answer about the pause before no longer counts; the maximum wait takes the turn anyway. A check that fails, or hasn't answered by then, leaves the maximum wait to decide, as it does for every turn while the check is down.
- One turn's speech runs to at most `VOICE_MAX_SPEECH`, a minute unless set; there the turn is taken with what was heard ([D22](#d22)).
- A spoken turn stays in flight until its reply's first audio is heard, or the reply stops with none; speech that begins before then is talking on: the turn is taken back and taken again with all the words ([D26](#d26)).
- `.claude/skills/measure/check-speak.sh` on an M3 Pro, Kokoro's sentences said into an audio connection: the reply's first audio went out 2.3 to 2.5 s after the speech ended with the fake model, 2.5 to 5.0 s with Sonnet 5.5 and 2.7 to 3.7 s with Haiku 5.5, the slowest each session's first turn.
- A turn with no words, as when only a cough was heard, isn't taken. Nor is one speech-to-text couldn't hear in full: it failed on an utterance, or a piece of an utterance's audio found 256 pieces, about 5 s, waiting for it, which counts as stuck, and the utterance was dropped through the swap point. An end or a drop that finds the queue full is held back until speech-to-text takes it, never lost, so both sides keep numbering utterances alike, and an utterance whose audio all got through is still heard; a new utterance's audio goes behind what is held. That turn ends at the pause, and the page gets the words heard so far, rough text included, to send or say again, with a short line offering typing. With the speech-to-text service down from the start, every utterance fails as its audio comes, so each turn ends so at its pause, and typed turns still work.
- A connection that ends while the user speaks, or before their turn is taken, takes nothing half heard: the page puts the words heard so far in the box, to send or say again.

A turn ends within its own deadline, `VOICE_TURN_TIMEOUT` (20s unless set, since a turn running that long means something is wrong), its last few seconds kept for saving how it ended; other requests keep 10s. After the deadline, a turn still in flight was cut off, by a crash say: the session's next turn saves it lost, so its words reach later prompts. When voice stops, it takes no new requests and cancels those in flight, so each turn ends as lost and its caller hears `lost`, rather than holding the stop for up to the turn's deadline; it waits at most 10s for them to finish.

1. The browser streams the user's audio to voice.
2. Speech-to-text returns rough text as it arrives, then final text.
3. The user pauses; the final text arrives and, at the same time, the end of turn decides the user has finished ([D22](#d22)), or the user hands over with a phrase ("your turn"); if not finished, voice keeps listening.
4. Once both are in, voice sends the turn to the model, with the screen input the page sent ahead ([D24](#d24), [D35](#d35)); starting it before the check answers is deferred ([D30](#d30)).
5. The model streams its reply; once the turn is confirmed, voice streams it to text-to-speech.
6. Text-to-speech turns it into audio, starting with a short first phrase, streamed to the browser, which plays it.
7. The turn is saved off to the side: what the user said, what the agent said as far as it was heard, the time of each stage ([D13](#d13)).

Failure modes:
1. Speech-to-text slow or down, from the start or mid-turn: no turn is taken; at the pause the page says so in a short line, puts the words heard so far in the box, and offers typing (above). The end-of-turn check slow or down: the maximum wait decides. A backup speech-to-text comes later.
2. Speech-to-text mishears a technical word: the live transcript shows what was heard; an agent can supply a vocabulary list where the implementation supports it.
3. The user keeps talking after the end of turn: [D26](#d26).
4. The model slower than usual: a visible thinking sign after a short wait; the delay shows in the timing.
5. The model errors, is down or rate-limited: a brief retry; then the agent says it lost the thread and asks the user to repeat; the user's words stay in the transcript. A second model provider comes later.
6. Text-to-speech fails: the reply still shows as text; retry, then the browser's built-in voice.
7. The connection drops mid-turn: the browser reconnects to any server ([D14](#d14)); the user's finished words are kept, and words still being heard go to the box, with no turn taken half heard; a half-delivered reply is saved whole, since nothing says how much reached the user.
8. The same turn arrives twice (a retry after a drop): recognised by its turn ID and answered once ([NFR5 safe retries](#nfr5)).
9. The same session open in two tabs: the newest tab takes over; the old one says so.
10. The conversation nears its size limit: compacted in the background before the limit ([D15](#d15)); a turn never waits for it. Until compaction is built, a turn whose request outgrows the model service's size limit ends lost with the kind `too_large`.
11. Someone else's session, or one that doesn't exist: refused; every turn checks workspace and user ([D12](#d12)), and a ticket for another session is answered as one that doesn't exist (404) before anything is read ([D32](#d32)).
12. Misuse (endless audio, huge typed input): typed words over `VOICE_WORDS_MAX_CHARS` are refused, and one turn's speech stops at `VOICE_MAX_SPEECH`, taken with what was heard (above). Limits per session come later.
13. The ticket passes its four hours while the session is live: a turn or an audio offer sent with it is refused (401 of type `/v1/problems/ticket-expired`), an offer a few seconds past by speech's own clock too, an audio connection it opened ends with the reason `expired`, a reply still playing cut where the user stopped hearing it and a turn still waiting not taken, and the page says the session has expired and to start a new one. Ending the session still takes the ticket for 15 minutes, so the page, learning its ticket ran out, sends End once with it, and the session ends at once with its summary rather than when the idle sweep finds it ([D32](#d32)).

People named in what a user says are affected too: their information reaches the model and speech providers, whose data terms are a decision for when a cloud provider is chosen.

#### Interrupt the agent ([U3](#u3))

The user interrupts the agent while it answers or speaks by sending a message, or by speaking over its voice for a fifth of a second ([D37](#d37)).

1. The browser stops playing the reply at once and sends the new turn on the data channel; or speech hears the user speak over the reply until the speech is held. Either is the interrupt; there is no separate message.
2. speech stops the reply: playback ends, text-to-speech stops making the next phrase, and a model still writing is stopped, the turn saved answered with the text so far; a turn not yet sent to the model never is. The user's words are saved whenever the interrupt comes.
3. speech works out how much of the reply was heard ([D36](#d36)).
4. conversation saves the turn cut: the whole reply stays, with how much of it was heard; a reply the model hadn't finished is cut even when heard to its last word, since it was cut short. Later prompts hold the reply only up to there, and the user's next message opens with a note that they cut in ([D24](#d24)).
5. speech tells the browser where the reply was cut, so the page shows the reply as the agent remembers it, then takes the new turn: a typed one at once, a spoken one once the user has finished it.

`.claude/skills/measure/check-speak.sh` on an M3 Pro, with the fake model, cut in on a reply as it began: the reply's last audio packet arrived 0.54 to 0.57 s after the first packet said over it (0.87 to 0.88 s with the interrupt speech at 0.5 s), of which about a third of a second was the sentence's quiet start, before speech detection heard speech, and 0.22 s the held speech; voice sent no packet of the reply after the interrupt, its audio stopping as the held speech reached voice, 30 ms after it was said.

Failure modes:
1. The interrupt comes before any sound: the turn is saved cut with nothing heard; its words stay, and the model sees them with no reply.
2. More turns come before the new one is answered: each interrupts the one before, and every turn's words are saved, in order.
3. The interrupt lands just after the reply finished playing: it was all heard; nothing is cut, and the new message is an ordinary turn.
4. Saving the cut fails: the new turn is still taken, and its prompt holds the whole reply; speech logs a warning.
5. A click, a breath or a cough over the agent: shorter than the interrupt speech, it is passed over and the agent speaks on; said as the reply ends, it is a turn. Held as long, it cuts in: the page is told the reply was interrupted, and nothing more, since a turn with no words heard isn't taken, so the agent waits for the user ([D37](#d37)).

<a id="d37"></a>
> #### D37. Speech held over the agent cuts in at once; shorter speech over it is passed over
> - Decision: speech is over the agent while the agent's reply reaches the user: from when its first packet would be heard until its last would, by the estimate the part heard is worked out by ([D36](#d36)), the connection's round trip plus 40 ms for the browser, counted from when the speech was said: 30 ms before it reached voice, the microphone's capture and Opus's 20 ms packets. Speech over the agent cuts in once it is held, `VOICE_INTERRUPT_SPEECH` of frames judged speech with no pause ([D20](#d20)): the reply stops at that moment through a typed turn's interrupt, saved cut as far as it was heard then, and the speech carries on as the next turn from its first word, its audio already with speech-to-text. Speech that began over the agent and pauses before it is held, the agent heard all through it, is passed over at its pause, a click, a breath or a short cough: it doesn't cut in and isn't a turn, its words are never shown or sent to the model, and nothing of it opens what the user says next. A word as short as "No" lasts the interrupt speech and cuts in on its own; what the user says after it joins its turn as any speech after a pause does. Speech the agent stopped being heard during, a "Yes." over the reply's last syllable, is a turn. The page is told of speech over the agent only once it is held. Speech that began while the agent wasn't heard is a turn however short, as before: after a reply has ended, and between a typed turn being sent and its reply being heard, where it interrupts when taken, as a typed turn does, and held speech cuts in only once the reply's audio reaches the user. Between a spoken turn being taken and its reply being heard, it is talking on after the end of turn ([D26](#d26)), which ends exactly where speech over the agent begins. A spoken turn begins on the goroutine that heard it, under the same lock as a typed one, so it has begun by the time it is handed over.
> - Needed by: [U3](#u3).
> - Assumes: Chrome's echo canceller keeps the agent's own voice out of the microphone ([D19](#d19), [A1](#a1)). Nothing guards against it failing: a fifth of a second of the agent's voice leaking in would stop every reply and become the user's turn. Checked by hand in Chrome with speakers on while the interrupt speech was 0.5 s, when no reply was cut off; not checked with speakers since it became 0.2 s. Mute is the user's way out. The page holds the microphone muted until the agent's greeting starts, since speech before its first sound would cut it ([U1](#u1) step 6). People talking to an agent leave out the acknowledgements, "mm-hm" or "right", they give a person while listening.
> - Why: stopping when the speech is held, not when its turn is taken, silences the agent about a fifth of a second after the user starts, not seconds later, after their sentence, the pause and the check. Speech over an agent is meant to stop it, since people don't acknowledge an agent as they do a person; 0.2 s, as long as Pipecat's speech detection needs speech before it reports the user speaking, and Vapi's default for how long the user speaks before its agent stops, lets a single "Wait.", about 0.3 to 0.4 s, cut in, while a click or a breath shorter than it doesn't. Judging by length needs no text, so it acts before speech-to-text has heard a word, and a page shown nothing until then never shows words that vanish. Judging over the agent by what reaches the user, not by a reply being under way, keeps an answer to an agent that has just stopped, and speech before it has said anything, as turns. The reply counts as heard up to the interrupt, since it played on until then.
> - Trade-off: a cough or a burst of noise that speech detection takes for speech, held a fifth of a second, cuts the agent off, and with no words heard no turn is taken, so the agent waits in silence for the user; an "mm-hm" said anyway cuts in and is answered; the agent talks over the user for that fifth of a second; short speech over the agent goes to speech-to-text as it is said, and is dropped at its pause before any final text is worked out; what reaches the user is estimated, a round trip out over the internet.
> - Alternatives rejected: cutting in as speech starts (a click or a breath stops the agent); a longer interrupt speech, 0.5 s, with short speech over the agent kept aside for a second to open a cut-in that followed (built first: a sharp "No" alone didn't stop the agent, and the agent talked over the user for half a second, to let through acknowledgements people don't give an agent); the agent going quiet at 0.2 s and speaking on if no word follows within about a second (the best for a false cut-in, and the most to build: the way to go if coughs or noise cut in, in use); cutting in on a word heard (about 0.6 to 0.8 s, waiting on speech-to-text); keeping short speech over the agent as a turn of its own, or as a note for the model; counting speech as over the agent from the turn being taken (a "Yes." as the reply ends is lost, and the agent waits in silence).
> - Status: accepted; the interrupt speech is tuned from measured sessions with the other speech settings, and revisited if coughs or noise cut the agent off in use.
> - Enforced by: tests, on a clock the test steps, that speech held over the agent stops its reply within a frame of the interrupt speech, and the microphone packet carrying it, and interrupts it at the moment it was held; that it reaches the page, and is taken as the next turn from its first word, only once held; that short speech is passed over, never asked about, told or taken, only while the reply is heard all through it, and is a turn when it overlaps the reply's end, comes before its first audio or after it ended; that the first packet counts only once heard, and a round trip moves where the reply stops being heard; that a blip over the agent is passed over at its pause, its utterance dropped with no final text asked for, and opens nothing after it, while speech right after it opens with the audio from where it ended; that two speech frames with quiet between over the agent don't cut in; that held noise over the agent with no words heard cuts in, takes no turn and sends the page no `hearing`; and a word lasting the interrupt speech cuts in on its own, what follows joining its turn; that speech-to-text failing on speech passed over fails no turn after it; that every turn closed at the cap is taken, in order; that a spoken turn has begun when it is handed over; speech's tests that held speech over a playing reply stops it, saved cut, then answers it, and that shorter speech lets the reply play to its end; `.claude/skills/measure/check-speak.sh` cuts in on a reply, by hand.

<a id="d36"></a>
> #### D36. How much of an interrupted reply was heard
> - Decision: the reply was heard up to the last audio packet sent before the interrupt arrived, less an allowance for the network and the browser: the round trip measured on the connection, plus 40 ms for the browser's buffer and its audio output. For an interrupt the user speaks, the interrupt is when the speech was said, 30 ms before it reached voice, for the microphone's capture and Opus's 20 ms packets. Phrases whose packets all went out by then were heard whole; of the phrase playing, the same share of its text as of its packets, moved back to a word boundary. A phrase whose packet count text-to-speech didn't give counts as not heard. The saved turn keeps the whole reply and how much of it was heard; one function gives the part heard, and every prompt uses it.
> - Needed by: [U3](#u3); [D24](#d24).
> - Assumes: a phrase is spoken at an even pace, so the share of its packets played is the share of its words; pauses at commas make it a word or two out.
> - Why: the model must not build on words the user never heard, and a question cut halfway should reach the model as half asked, not missing; later phrases are whole sentences, so counting whole phrases only would drop about half a sentence the user heard. The server knows what it sent and when, so no client has to report anything. Keeping the whole reply keeps what the agent didn't get to say, the evidence for tuning an agent that talks too long.
> - Trade-off: about a word or two out within a phrase; over the internet the round trip is about a word, which the allowance only estimates; the turn after an interrupt starts a fresh session process in the model service, since the reply it wrote is no longer the one recorded ([D25](#d25)).
> - Alternatives rejected: whole phrases only (drops the part of a sentence the user heard); word timestamps from text-to-speech (Kokoro's server gives them only on an endpoint it marks for development, and every vendor behind the swap point would have to; the way to go once a vendor that streams them is chosen); the browser reporting its playback position, the RTP timestamp of the last frame it played (every client would have to send it, and neither the page nor the drop-in client does yet); overwriting the saved reply with the part heard (loses what the agent didn't say).
> - Status: accepted.
> - Enforced by: tests that an interrupt stops the reply without losing the user's words; that the part heard is saved and the whole reply kept, a point inside a character moved back to its start, and that only the last finished turn of a session, in its own workspace, can be cut; that the database refuses a heard count that contradicts its turn; and that a cut reply reaches later prompts as far as it was heard, with the note opening the next user message; table tests of the estimate (a phrase heard whole, the share of the phrase playing back to its last whole word, a point inside a character, a phrase of unknown length, more packets than a phrase said, and the round trip moving the point back across a phrase's end); a test that a real connection measures its round trip; a test that Kokoro's packets carry their phrase's packet count; speech's tests that the turn after an interrupt is asked with only the part heard, and with the whole reply when saving the cut failed.

<a id="turns-limitations"></a>
### Limitations and open questions

- Which model the reference agent uses: it starts on Sonnet 5.5 with thinking off, and Haiku 5.5 is timed against it on the same turns; the timing runs set the service's model and publish the agent without its own ([D25](#d25)).
- The pause and maximum wait values: starting values, tuned from the first measured sessions ([D20](#d20), [D22](#d22)).
- Detection in the browser: added if interrupting feels sluggish in the timing ([D20](#d20)).
- The speculative start: built when the end-of-turn check answers after the final text, as measured with `.claude/skills/measure/check-speak.sh` ([D30](#d30)).
- govad without the 64 samples of context Silero's own wrapper gives the model: given that context, by a patch to govad, if pauses or interrupts misfire at the edges of speech in use ([D20](#d20)).
- A reply whose connection drops part way is saved whole; saving it as far as it was heard needs the browser to say what it played, which neither the page nor the drop-in client does yet ([D36](#d36)).
- Swapping speech-to-text or text-to-speech: when timing shows either is the slowest stage, or when Japanese is built ([D21](#d21), [D23](#d23)).
- Nothing guards against the agent's own voice leaking into the microphone: a fifth of a second of it judged speech would stop every reply. Checked by hand in Chrome with speakers on while the interrupt speech was 0.5 s, not since it became 0.2 s; Mute is the user's way out ([D37](#d37)).
- A cough or a noise held as long as the interrupt speech, or an "mm-hm", cuts the agent off. The agent going quiet and speaking on when no word follows, or judging such sounds by their text, wait until that happens in use ([D37](#d37)).
- When text-to-speech fails mid-reply, the rest of the reply arrives as text only, and the page says the audio stopped. Asking again for the failed phrase, and then falling back to the browser's own voice, wait until failures show up in real sessions ([D23](#d23)).

<a id="user-context"></a>
## 10. User context

<a id="context-overview"></a>
### Overview

User context loads what an agent may know about the user for a session, and saves what a session learned. Each agent declares the context kinds it reads (a profile, past-session summaries, its own records); user context loads only those, most relevant first, up to a size limit.

<a id="context-requirements"></a>
### Requirements

- <a id="u7"></a>U7. See, correct and delete what voice-guide knows about me. Caller: a user.
- <a id="u8"></a>U8. Switch to another agent and have it know me. Caller: a user. Needs a second agent.
- <a id="w2"></a>W2. Grant another workspace access to my context. Caller: a user. Excluded for now.

<a id="context-how"></a>
### How it works

<a id="d27"></a>
> #### D27. An agent reads only the context kinds it declares
> - Decision: user context holds what voice-guide learns about a user in conversations, such as session summaries; what a product already knows about the user arrives from the product as session context ([D35](#d35)), so a resume is never a user context kind. The kinds are `profile` and `session_summary`; a session summary is read only by the agent that wrote it ([D39](#d39)). Each agent declares which context kinds it reads; user context loads those kinds for the session, most relevant first, up to a size limit ([D34](#d34)), and the declaration is the privacy rule. A context item is never edited: a change is a new item that replaces the old one, so a session can tell that what it loaded is no longer current, and the change reaches it at its next turn. Lookups during a session come later, for large material.
> - Needed by: [U1](#u1); [U8](#u8); [NFR3 privacy](#nfr3).
> - Assumes: the kinds an agent needs are known when it is defined.
> - Why: the user and the reader of an agent's definition can both see exactly what it reads; loading less keeps the prompt fast ([D24](#d24)).
> - Trade-off: an agent that needs a new kind needs its declaration changed, and a new kind is a migration; every change keeps the item it replaced until that is deleted.
> - Alternatives rejected: every agent reads everything about the user (no privacy rule, slower prompts); editing an item in place (a running session couldn't tell its copy is out of date).
> - Status: accepted.
> - Enforced by: a test that user context returns no kind the request didn't name; the table's check on kinds; a database trigger refusing any edit of an item, and a unique, same-user, same-kind reference from a replacement to the item it replaces, with tests.

<a id="d34"></a>
> #### D34. Loading chooses whole items, in the declared order, within a character limit
> - Decision: user context loads the requested kinds in the order the agent declares them, newest first within a kind, whole items only. An item that doesn't fit what is left of the limit is left out and the next is tried, and the IDs left out come back with what was loaded. The limit counts characters and is the caller's: voice sends it with every load. A load reads at most one item per 100 characters of the limit, and at least 20, of each kind, the newest, so a start never reads every summary a user ever had: 200 at the 20,000-character default, ten times the summaries that fit (a round guess, checked when kinds with short items are added). Items past that, each older than every item of its kind that was read, aren't read, so they aren't among the IDs left out: listing them would mean reading them.
> - Needed by: [U1](#u1) failure mode 4; [D24](#d24) (the user-context layer has a known size).
> - Assumes: an agent's author ranks its kinds, and within a kind the newest matters most; until an agent declares more than fits, no scoring is needed.
> - Why: the simplest rule a reader of the agent's definition can predict; characters cost nothing to count and need no tokenizer per model.
> - Trade-off: characters only approximate tokens (about four to a token in English, nearer one in Japanese), so the limit is revisited with Japanese; an old item never loads while newer ones fill the limit.
> - Alternatives rejected: cutting an item to fit (a resume cut mid-sentence misleads the agent); scoring relevance now (nothing yet to score against).
> - Status: accepted.
> - Enforced by: tests of the order, the limit across all loaded items, an item exactly at the limit and one a character over, characters rather than bytes, and the IDs left out.

<a id="history"></a>
## 11. History

<a id="history-overview"></a>
### Overview

A product's server reads back its users' past sessions: what was said, and what each session concluded, for the product to show its user or act on.

<a id="history-requirements"></a>
### Requirements

- <a id="u6"></a>U6. Review past sessions. Caller: a product's server, for one of its users.

<a id="history-how"></a>
### How it works

#### Review past sessions ([U6](#u6))

1. The product's server lists a user's sessions: `GET /v1/users/{user}/sessions`, with its product key, where `{user}` is its own reference for the user, never voice-guide's user ID ([D32](#d32)). The reply is the user's sessions in the workspace, newest first, each with its ID, the reference, its agent and version, when it started, when it ended and its summary, the last two null while it is live and the summary null when none was written ([D39](#d39)); no turns.
2. A page is at most `limit` sessions, 1 to 100, 20 without one. `before`, a session's ID, starts the page after it: session IDs are UUIDv7, so ID order is start order, and the cursor needs no table of its own. A page shorter than its limit is the last. A `limit` or a `before` that isn't what it should be is refused (400).
3. The product's server reads one session: `GET /v1/sessions/{id}`, with its key. The reply is the session as above and its turns in order, as the user heard them, in the shape of a delivery's ([D40](#d40)): `seq`, the user's `words`, the `reply` as far as the user heard it and whether it was `cut`. Unlike a delivery, the reply is never cut to a size: the product asked for the whole session, and a two-hour session of a turn every half minute read about 220 KiB in the test that looks at it.

Failure modes:
1. Another workspace's session, or one that doesn't exist: 404, both alike, so a caller learns nothing about sessions outside its workspace. A user reference the workspace never started a session for: an empty list, which looks the same as a user with no sessions yet.
2. A ticket, or no credential: 401, as on any route a product's server calls ([D32](#d32)). The user's browser never reads past sessions: showing them is the product's page's work, through its own server.
3. A session still live: listed and read with its end and summary null, and the turns finished so far; the summary exists only after the session ends and its job has run, so an ended session may still show a null summary for some seconds ([D39](#d39)).
4. A user with a long history: a page costs one query by the user's newest sessions, which an index on the workspace, the user and the session's ID serves, and one small read of the summary for each ended session in it, at most 100.
5. A request that fails inside voice-guide: 500 with no reason, the reason in the log.

Every query filters by workspace and, for a list, by the user the workspace's own reference names; a test against PostgreSQL shows another workspace's product reads none of this one's sessions and lists none of its users.

<a id="timing"></a>
## 12. Timing and load

<a id="timing-overview"></a>
### Overview

The team running voice-guide sees where each turn's time went, and runs load tests that simulate many users without spending on providers.

<a id="timing-requirements"></a>
### Requirements

- <a id="o1"></a>O1. See per-stage timing for any turn; run a load test. Caller: the team running voice-guide. Timing is recorded from the first build ([D13](#d13)); the view and the load tests come later.

<a id="timing-how"></a>
### How it works

<a id="d28"></a>
> #### D28. Load simulated locally with fakes
> - Decision: load tests run locally against fakes for the model and speech, so simulating 1,000 users costs nothing.
> - Needed by: [NFR2 scalability](#nfr2); [O1](#o1).
> - Assumes: the fakes behave like the real services in timing shape, not in content.
> - Why: shows where voice-guide's own code and database stop keeping up, without a provider bill.
> - Trade-off: provider limits and real latencies are not tested.
> - Alternatives rejected: load tests against real providers (cost, rate limits).
> - Status: accepted.
> - Enforced by: the model's fake provider ([D25](#d25)).

<a id="appendix-a"></a>
## Appendix A. Terminology

The terms used in code, docs and the interface, each with one meaning; no synonyms. Code names are settled with the first code that uses each term.

| Term | Meaning | Not |
|---|---|---|
| agent | The AI that talks: persona, rules, activity, the context kinds it reads, voice settings | coach, assistant, bot |
| agent file | The Markdown file in `agents/` that an agent's versions are published from: a settings block, then the persona | seed, definition file |
| screen input | What a product sends about what the user is doing right now (the page, a board, an attached file), as named text blocks, with a turn or ahead of the next ([D35](#d35)) | contextual update, page state |
| session context | What a product sends when it starts a session about what it already knows of the user (a resume, a plan), as named text blocks the agent's version declares ([D35](#d35)) | variables, metadata, dynamic variables |
| catalog | The agents one workspace owns, as the agents module offers them; new sessions get each agent's newest version from it | registry, library |
| voice | The part of voice-guide that gives an agent a voice: the conversation and speech modules together; configurable, but doesn't decide how it is used | pipeline |
| a voice | The sound replies are spoken in, such as `af_heart`, as the text-to-speech service names it; an agent file or a session's start chooses one, with a speed ([D23](#d23)); told apart from voice, the platform part | speaker, persona (which is the agent's text) |
| conversation | The module that runs sessions and their turns: each turn's prompt, the model's reply, what was said; text only, whether the words were typed or spoken | dialogue, chat |
| user context | The module that loads what an agent may know about the user for a session, and saves what the session learned; Go package `usercontext` | guide, memory, hub |
| workspace | Owns agents and is responsible for them; an individual or a company | organization, tenant, builder |
| user | The human who talks to an agent in a session | person, candidate, student, customer |
| product | A workspace's own application that puts its agents in front of its users: its server starts sessions with the product key, and its pages hold the conversation; voice-guide's own web page is one ([W3](#w3)) | client, app, integration, tenant |
| product key | The secret a product's server sends to prove which workspace it acts for; voice-guide keeps only its hash ([D32](#d32)) | API key, secret key |
| user reference | A product's own stable ID for one of its users (not a name or an email address), sent when its server starts a session; it names the same user every time ([D32](#d32)) | external ID, customer ID |
| webhook | Where a workspace's product hears that its sessions ended: a URL on the product's server, and the secret deliveries to it are signed with ([D40](#d40)) | callback, hook, endpoint |
| delivery | One ended session posted to a workspace's webhook, with an ID kept on every attempt so the product's server drops a repeat ([D40](#d40)) | event, notification, message |
| ticket | What the user's browser holds for one session: signed by voice-guide when the product's server starts the session, it works only in that session, until it ends or four hours after it started ([D32](#d32)) | session token, access token |
| drop-in client | The script voice serves at `/client/voice-guide.js` that a product's page loads to hold the conversation in a session its server started ([D41](#d41)) | widget, SDK, embed code |
| member | A human in a workspace who builds or manages its agents | admin, builder |
| speech detection | Judging, frame by frame, how likely the user's audio holds speech, so voice notices them speaking and pausing ([D20](#d20)) | VAD, voice activity detection |
| speech | Speech-to-text and text-to-speech together; also the module that runs them with the audio connection, telling when the user has finished, and interrupting | voice (which is the platform part) |
| audio connection | A browser's WebRTC connection for one session's audio, held by the server that answered it | call, stream, peer |
| utterance | The user's speech from when speech detection hears it start until a pause, sent to speech-to-text as one item; a turn's words are the final texts of one or more | segment, stretch |
| phrase | A part of an agent's reply that text-to-speech speaks in one go, marked on each of its audio packets with where it ends in the reply; how a reply is cut into phrases is each text-to-speech implementation's choice ([D23](#d23)) | piece, chunk, segment |
| session | One conversation between a user and an agent version | call, chat |
| turn | One exchange in a session: the user's words, the agent's reply, the time of each stage | message |
| opening | The turn an agent that speaks first opens its session with: the first, with no words from the user, its greeting written from a fixed cue ([D18](#d18)) | greeting turn, first message |
| talking on | The user speaking again after their spoken turn was taken and before any of its reply was heard: the turn is taken back and taken again with all their words ([D26](#d26)) | continuation |
| interrupt | The user cutting in while the agent answers or speaks: by a typed turn, or by speech held over the agent ([D37](#d37)) | barge-in, stop |
| cut | Said of a turn the user interrupted: its whole reply kept, with how much of it they heard ([D36](#d36)); not a turn that outlived its server, which is lost | truncated |
| context item | Something user context holds about a user, of a declared kind | memory, fact |
| job | A piece of background work in the queue, of a kind one handler runs, with that kind's args ([D15](#d15)) | task, message |
| lease | How long a claimed job is held before another run may claim it ([D15](#d15)) | lock, timeout |
| summary instruction | What an agent's file asks to be noted about one of its sessions once it ends ([D39](#d39)) | summary prompt, analysis plan |
| swap point | An interface with implementations chosen by configuration | plugin |
| fake | A working implementation of a port that takes a shortcut, such as set replies at a set pace; the core's tests and load tests use it | stand-in, dummy, mock, stub |
| session process | The Claude Code process the model service keeps for one session between turns; a disposable cache of the conversation, replaced whenever it no longer matches | warm process, held session |
| provider | One way the model service reaches a model, named for whose model and how it is paid for (`claude-subscription`, `claude-api`), or `fake` | the library it uses (agentsdk) |

<a id="appendix-b"></a>
## Appendix B. Conventions

### Identifiers

Every identifier links to its single definition, with its name beside it.

| Prefix | Meaning | Defined in |
|---|---|---|
| U | A use case of a user | Its feature's "Requirements"; listed in [section 3.1](#use-cases) |
| W | A use case of a workspace's member | As U |
| O | A use case of the team running voice-guide | As U |
| F | A failure family that applies across use cases | As U |
| NFR | A quality | [Section 5](#qualities); targets in each feature's "Qualities" |
| A | An assumption | The first section that relies on it |
| D | A design decision | Where the choice is made; indexed in [Appendix C](#appendix-c) |

Identifiers are numbered in the order they were created and never reused. A reversed decision stays, marked "superseded by" its replacement.

### What counts as a decision

A choice is a decision if a reviewer could reasonably ask "why not X?". Every such choice gets a decision box with these fields, in this order:
- Decision.
- Needed by.
- Assumes.
- Why.
- Trade-off, when it costs something worth stating.
- Alternatives rejected.
- Status: accepted, or proposed with what settles it.
- Enforced by: the check that fails when the decision is broken, or "guidance only" with the reason. Technology choices are rows of a table with the same reasoning in fewer columns.

### Lists

The qualities in [section 2.2](#what-it-is) are ordered by importance, most important first.

### Scope

Included: built and tested as part of voice-guide now. Excluded for now: where it grows next; not built or designed, and the code avoids blocking it where that costs little. Ideas nobody plans to build are not written down.

<a id="appendix-c"></a>
## Appendix C. Decision index

In reading order; labels are kept when decisions are added later.

| ID | Decision | Where |
|---|---|---|
| [D1](#d1) | One service with four fenced modules | [How it works](#modules) |
| [D2](#d2) | Every component behind a swap point | [How it works](#modules) |
| [D31](#d31) | Hexagonal inside each module | [How it works](#modules) |
| [D3](#d3) | One user account; records owned by the workspace holding them | [How it works](#domain) |
| [D4](#d4) | Go for the core service | [Platform](#technology) |
| [D5](#d5) | PostgreSQL 18, the only data store at first: plain tables for the core, JSON columns for per-agent records, pgvector for relevance search | [Platform](#technology) |
| [D6](#d6) | sqlc with pgx for queries; goose for migrations | [Platform](#technology) |
| [D7](#d7) | UUIDv7 for every ID | [Platform](#technology) |
| [D8](#d8) | React with TypeScript in the browser, calling the Go service directly | [Platform](#technology) |
| [D9](#d9) | Python for the speech-to-text service; TypeScript for the model service | [Platform](#technology) |
| [D10](#d10) | Every part in a container; docker compose locally, the same containers in the cloud; settings from the environment | [Platform](#technology) |
| [D29](#d29) | An API for outside products; voice-guide's own web page is its first client | [Platform](#platform-api) |
| [D11](#d11) | HTTP for agents and sessions, WebRTC for audio | [Platform](#platform-api) |
| [D33](#d33) | Errors as problem details; the version in the path | [Platform](#platform-api) |
| [D12](#d12) | Workspace on every record, user on every record about one; every query filters by workspace but the system's sweeps and the key check | [Platform](#platform-data) |
| [D32](#d32) | A product's server starts each session with its key; the user's browser holds only what voice-guide signed | [Platform](#platform-security) |
| [D41](#d41) | A session's routes and the drop-in client answer pages on any site; a product's routes answer none | [Platform](#platform-security) |
| [D13](#d13) | Every turn timed by stage | [Platform](#platform-observability) |
| [D14](#d14) | No conversation held in a server's memory | [Platform](#platform-scale) |
| [D15](#d15) | Slow work as background jobs, in one PostgreSQL queue for every kind | [Platform](#platform-scale) |
| [D16](#d16) | The reference agent: a mock interviewer | [Agents](#agents-how) |
| [D17](#d17) | Agents stored as versions that never change, published from one file per agent | [Agents](#agents-how) |
| [D18](#d18) | An agent that opens warms the model's cache with its opening | [Sessions](#sessions-how) |
| [D35](#d35) | The product sends what it knows when a session starts, and what its screen shows with each turn | [Sessions](#sessions-how) |
| [D38](#d38) | A session ends when the user ends it, or after five minutes idle | [Sessions](#sessions-how) |
| [D39](#d39) | A session's summary: written by the model its agent names, with the agent's instruction, read back only by that agent | [Sessions](#sessions-how) |
| [D40](#d40) | A product's server hears each ended session at its webhook: signed, after the summary, the same delivery ID on every attempt | [Sessions](#sessions-how) |
| [D30](#d30) | Start the model before the end of turn is confirmed | [Turns](#pipeline) |
| [D19](#d19) | Audio over WebRTC, in the Go service | [Turns](#transport) |
| [D20](#d20) | Speech detection on the server | [Turns](#noticing-speech) |
| [D21](#d21) | Moonshine in a small Python service with OpenAI's realtime transcription API | [Turns](#speech-to-text) |
| [D22](#d22) | End of turn: a pause, then Smart Turn, then a maximum wait | [Turns](#end-of-turn) |
| [D23](#d23) | Kokoro on the server, short first phrase | [Turns](#text-to-speech) |
| [D24](#d24) | A layered, cached prompt | [Turns](#the-model) |
| [D25](#d25) | Model access behind a provider-neutral swap point | [Turns](#the-model) |
| [D26](#d26) | Talking on after the end of turn: merge, until the agent speaks | [Turns](#the-model) |
| [D37](#d37) | Speech held over the agent cuts in at once; shorter speech over it is passed over | [Turns](#turns-use-cases) |
| [D36](#d36) | How much of an interrupted reply was heard | [Turns](#turns-how) |
| [D27](#d27) | An agent reads only the context kinds it declares | [User context](#context-how) |
| [D34](#d34) | Loading chooses whole items, in the declared order, within a character limit | [User context](#context-how) |
| [D28](#d28) | Load simulated locally with fakes | [Timing and load](#timing-how) |
