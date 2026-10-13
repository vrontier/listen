# Design: Listening Observatory Workbench

Status: draft (2026-10-13). Nothing here is built yet.

Each statement in this document is marked by where it stands: **decided** (agreed, build to it), **proposed** (a suggestion that has not been confirmed) or **open** (a question without an answer).

## Why a separate project

Listening Observatory began as one site that would grow a community section: accounts, own streams, own analysis settings. That would have put personal data, access tokens and legal obligations into what is otherwise a small public exhibit.

**Decided:** the two concerns are split.

| | Listening Observatory | Workbench |
|---|---|---|
| Purpose | show a few streams to the public | support individual research |
| Streams | curated by the maintainer | brought by researchers |
| Accounts | none | yes |
| Analysis per stream | one, configured by the maintainer | several, one per researcher or project |
| Filters | 1 to n rules per stream, set by the maintainer | defined by each researcher |
| Personal data | none | email address, display name, tokens |
| Obligations | credits per source | privacy notice, legal notice, moderation |

What this gives:

- The Observatory stays small. Nothing there needs a privacy notice or moderation.
- Everything with obligations sits in one place.
- Consent gets simpler: a researcher works on a stream they run or have permission for.

## The three pieces

**Decided:** there are three pieces, not two.

```text
                 ┌──► Listening Observatory   curated public exhibit, no accounts
listener ────────┤
(shared engine)  └──► Workbench               accounts, tokens, own streams, own analyses
```

- **The listener** is the engine: it connects to a stream, decodes it, analyses it, remembers motifs and serves the results. It lives in the Observatory's repository (`github.com/vrontier/listen`, directory `listener/`). Profiles and defined motifs, as drafted in the Observatory's `docs/profiles-and-community-spec.md` (Parts A and B), belong to it.
- **The Observatory** is the showcase on top of it.
- **The Workbench** is the layer for accounts, tokens and configuration on top of the same listeners. The community section of that spec (Part C) and the Observatory's `docs/user-streams-design.md` move here; see [members-and-streams.md](members-and-streams.md).

A separate, third repository holds offline work on recordings (for example decoding weak digital radio signals). That is not stream observation and does not shape this design. Its results may appear on the Workbench's public research pages.

## Design rules

**Decided**, all four.

### 1. One shared engine

Analysis code is written once, in the listener. The Workbench must not carry its own copy of the DSP, the detectors or the memory, or the two projects drift apart and results stop being comparable.

### 2. One connection per stream

A stream's operator sees one connection from this system, whatever the number of researchers working on it. Per-researcher filters and motifs run as additional analyses on one shared decode, with a cap on how many may run per stream.

Without this rule the Workbench would multiply the load on operators' servers, most of which are run by individuals.

### 3. The Observatory's filters never apply to the Workbench

A filter rule is a property of one analysis, not of the stream. The Observatory removes known interference so that its public page shows what the maintainer considers the signal. A researcher may be studying exactly that interference, and must be able to see it.

So the Workbench takes raw audio, or runs its own analysis with its own rules or with none. It does not read the Observatory's event stream or its MCP server for research, because parts of that data are already filtered. Which parts is listed in [analyses-and-filters.md](analyses-and-filters.md).

### 4. Researchers bring their own streams

A stream is added by the person who runs it, or with the explicit permission of its operator. No answer from an operator means no. The Workbench does not offer a catalogue of third-party streams to pick from.

"Select and gather streams" in the feature list therefore means: a researcher's own streams, and streams whose operators have agreed to their use in the Workbench.

## Dependencies

Rule 1 makes the Workbench depend on the Observatory's repository; that much is **decided**. The form of the dependency described here, one engine process next to the Workbench, follows from the process split below and is **proposed** with it.

**What the Workbench requires:**

- **The engine process.** One program built from `github.com/vrontier/listen`, directory `listener/`, running on the same host next to the Workbench's own process. See [Processes](#processes).
- **ffmpeg** on that host. The engine decodes every stream with it.
- **A Go toolchain** to build both programs (the engine's module asks for Go 1.23 or later). The built programs need no Go on the server.

**What it does not require:**

- A deployed Listening Observatory. The Workbench needs none of the Observatory's website, its source list, its per-stream listeners or its narrator setup. A Workbench can run on a server where no Observatory exists.

**When the two share a host:**

- Only if the Workbench is to analyse streams that the Observatory also analyses. Rule 2 then requires that one engine process holds the connection for both, so both run on the same host and use the same engine process. Whether the Workbench does this at all is **open** (question 1).

**Consequence:** anyone running a Workbench fetches and builds from two repositories. Moving the engine into a repository of its own is a possible later step. It is not planned now.

**What exists today:** the engine process the Workbench needs is not built. Today's listener runs one analysis of one stream per process, is started with command-line flags, and has no interface for adding a stream or an analysis while it runs.

### Staying in step

**Proposed.** The two repositories change independently, so the Workbench must be able to tell whether the engine next to it fits.

- The control API (see [Processes](#processes)) has a version of its own with two numbers. The first is raised for a change that breaks existing callers, the second for an addition.
- The engine process reports this version and its own build version on the control API.
- The Workbench asks at start and whenever it reconnects. If the first number differs from the one it was built for, or the second is lower than it needs, it refuses to change anything in the engine, says so on its admin page and in its log, and keeps serving pages.
- This repository states the minimum engine version it needs, in one place (a line in the README or a small file), and the control API spec in `docs/` carries the API version it describes.
- The engine's repository tags the commits that change the control API version, so "minimum engine version" names something that can be checked out.

## Processes

**Proposed**, all of this section. It comes from a review of the engine's code on 2026-10-13.

```text
browser, programs, AI assistants
        │
        ▼
web server ──► Workbench process ──control API, local socket──► engine process ──► streams
               (this repository)                               (listen repository)
               pages, catalogue, login,                        connections, decodes,
               tokens, data stream, MCP                        analyses, memory
```

### Two processes

- **The engine process** lives in the listen repository. It holds the streams, the decodes, the analyses and their memory. It knows nothing about users, accounts, tokens or publication. It is reachable on a local socket only.
- **The Workbench process** lives in this repository: one Go binary with the pages, the catalogue, the login, the tokens, the data stream and the MCP endpoint. Everything that reaches the outside goes through it, and it alone decides who may see what.

### Between them

- **A control API on a local socket.** The Workbench tells the engine which streams and analyses should exist and with which settings. The engine reports their state. The same socket carries the analysis events that the Workbench passes on. Its draft is [control-api.md](control-api.md); it is to be settled before either side is built (question 4).
- **No shared database.** The catalogue belongs to the Workbench alone. The engine never reads it. This replaces "the hub reads the catalogue" of the earlier drafts. The reasons: a shared file would tie a database schema to two repositories, and it would put account data within reach of the engine.
- **The engine validates.** Filter rules, analysis settings and motif definitions are checked by the engine, which is asked through the control API before anything is saved. The Workbench therefore imports no engine code and keeps no second copy of the rule syntax, which is what rule 1 asks for.
- **Importing is not possible anyway.** All of the engine's packages sit under `listener/internal/`, and Go refuses imports of an `internal/` directory from another module. A program built from those packages has to live in the listen repository, unless the engine publishes packages for others to use.

### The name

Earlier drafts call the engine process "the hub" or `listen-hub`. The engine already has a package `internal/hub`, which is the WebSocket fan-out of one analysis. The same word for two different things in one code base would confuse. These documents say **engine process**; a working name for the program is `listen-engine`. The final name is the listen repository's to choose.

### Go rather than PHP for the Workbench

The Observatory's site is vanilla PHP behind NGINX. The proposal for the Workbench is Go, with server-rendered pages, plain JavaScript, vendored dependencies and no build step for the front end.

Reasons:

- **Long-lived connections.** The data stream and the MCP endpoint are connections that stay open for minutes or hours and must be checked against a session or a token. Under PHP-FPM each of them occupies one worker for its whole life.
- **One place that checks access.** With PHP pages and a separate Go program for the long-lived connections, two programs would have to verify sessions and tokens, and two would write to the catalogue. One process is simpler to reason about and to secure.
- **One toolchain.** The engine is Go. The Workbench shares no code with it, but the same language, test tools and build serve both.

What speaks against it:

- Pages and forms are quicker to write in PHP, and the Observatory's site already has a contact form and mail setup to borrow from.
- The maintainer then keeps two kinds of site.

Deployment is not a reason either way. A Go binary needs a one-time root step on a server for its service unit, its user and its web server routes, just as a PHP site needs one for its pool and its routes.

### Server-Sent Events for the data stream

- The engine's event stream is one-way today, server to client, so SSE loses nothing against WebSocket.
- SSE is plain HTTP: it passes proxies, works with `curl`, and a program sends its token in a request header.
- A browser page uses the session cookie, since a browser's `EventSource` cannot set headers.
- Every event carries a sequence number, so a client can see that it missed something. Replaying missed events is not offered: the engine keeps no backlog.
- WebSocket is not offered at first. Relaying audio, if it is ever allowed (question 10), would need a channel of its own.

## Features

**Decided** as the scope. The detail of each is **proposed** or **open** as noted.

| Feature | What it means | State of the detail |
|---|---|---|
| Public presentation of research | Pages that show ongoing work: the stream, the researcher's analysis, their notes | open: page design, who may publish what |
| Member area | Log in, manage streams, filters, motifs and tokens through forms | proposed in [members-and-streams.md](members-and-streams.md) |
| Streams | Register a stream, probe it, run it on demand | proposed, same document |
| Filters and motifs | Define what an analysis ignores and what it looks for | filters: rule syntax exists in the listener; motifs: drafted as "defined motifs" in the Observatory's spec |
| Data stream by token | The analysis events of one's own analyses | proposed: SSE, see [Processes](#processes); open: how the token is passed, limits |
| MCP by token | The same data and the motif memory for AI assistants | proposed: an HTTP endpoint in the Workbench process; open: authentication detail |

What exists today that these build on:

- The listener serves its analysis as a WebSocket event stream (`ws/live`) and a small HTTP API, both without authentication, on a local port behind a web server.
- A read-only MCP server (`listen-mcp`) runs next to the listeners and is reached over SSH. It has no tokens; access is whoever has the SSH key.
- Filter rules exist as the listener's `-ignore` option.
- One listener process runs exactly one analysis of one stream.
- ffmpeg opens the connection to the stream and reconnects by itself. The listener does not see the HTTP exchange.

## What the engine must learn first

**Decided** as a dependency: rule 2 and rule 3 together need "one decode, several analyses" in the listener. Today the only ways to get a second analysis of a stream are to read the first one's (filtered) events or to start a second listener, which opens a second connection to the operator. Neither is acceptable for research use.

The review of the code showed that this is two pieces of work, not one. The split is **proposed**.

### Piece A: one process, many streams, changed while it runs

Today one process is wired for one stream: one event numbering, one memory, one fan-out to clients, one status, one HTTP port, all set from command-line flags. Piece A turns this into a process that holds many streams, each with that set of parts, and adds, changes and removes them through the control API. This is the larger piece. It is the "hub" of the earlier user-streams design and it ends the root step per stream.

With piece A alone, each stream still has exactly one analysis. That is enough for a member who registers their own stream and analyses it one way.

### Piece B: several analyses on one decode

The analysis itself is already an object that is fed samples, so feeding two of them from one decode is a small change. What it brings with it:

- **Sample rate belongs to the decode.** ffmpeg resamples to the rate the listener asks for, so the rate is fixed before any analysis sees a sample. It has to become a property of the stream, not a setting of an analysis. See [analyses-and-filters.md](analyses-and-filters.md).
- **The memory needs the analysis in its key.** Today the memory's directory is named after the source, the input and the range and rules. Two analyses of one stream with the same range and rules would write to the same directory.
- **A cap** on analyses per stream, and a measured cost of one more analysis. Each analysis computes its own spectrum today.

Piece B is needed as soon as one stream has a second analysis: a member comparing two settings on their own stream, a second member on the same stream, or the Observatory and the Workbench on the same stream.

### A third change, for safety

The limits in [members-and-streams.md](members-and-streams.md) ask that a stream's address is checked again on every redirect and reconnect. That cannot be done while ffmpeg owns the connection. The engine would have to open the HTTP connection itself and hand the bytes to ffmpeg. The Observatory's draft on station time asks for the same change for another reason. It is needed before strangers can register stream addresses, not before the first private use.

## Order of work

The order is **open**. Two views are on record:

- The maintainer expects the member area to be needed early, because the researchers are not all programmers. A token and a WebSocket URL are of little use to someone without a client.
- The earlier proposal was to start with tokens on the existing listeners, as the smallest step from what runs today. Since the rules were decided this no longer holds: the existing listeners are the Observatory's filtered analyses (rule 3), and extra listeners for research would be extra connections (rule 2).

A **proposed** order that respects the first view:

| Step | Content | Depends on |
|---|---|---|
| 0 | The control API, written as a spec in this repository: [control-api.md](control-api.md), a draft | — |
| 1a | Engine, piece A: one process, many streams, added and removed at run time through the control API | 0 |
| 1b | Engine, piece B: several analyses on one decode | 1a |
| 2 | Catalogue and accounts: login by email link, own streams, private pages | 1a; privacy notice and legal notice |
| 3 | Filters per analysis, set in a form; the page shows which rules are applied | 2; a second analysis on one stream needs 1b |
| 4 | Tokens: data stream and MCP for one's own analyses | 2 |
| 5 | Defined motifs per analysis; captures and trial runs | the listener's defined-motif work |
| 6 | Public research pages | 2, and a decision on publishing |

Step 2 can be built against a stand-in for the engine as soon as step 0 is stable. The engine opening the stream connection itself (see above) has to be done before sign-up is open to people the maintainer does not know.

## Open questions

Recommendations are the reviewer's, from the review of 2026-10-13. They are **proposed**, not decided.

### For the maintainer to decide first

These four shape the architecture.

1. **Whose streams?** Members' own streams only at first, or the Observatory's streams too? The second would move the Observatory's production listeners into the shared engine process, and each operator would have to agree to the new use.
   - *Recommendation:* members' own streams only at first. The engine process is then proven on streams whose operators are the members themselves, and the Observatory's production setup stays untouched. Taking in the Observatory's streams is a later decision of its own.
2. **On demand or continuous for private streams?** The draft runs a private stream only while its owner watches or a token reads. Learned motifs need continuous listening: a memory built from scattered minutes shows what happened to be heard, not what recurs.
   - *Recommendation:* the owner chooses per stream, on demand by default. Continuous listening is allowed on a stream the member runs themselves, within a cap on continuously running streams. Every analysis page states the mode and which hours were actually heard, so that a memory with gaps is not misread.
3. **Who may add an analysis to someone else's stream, and what is published: a stream or an analysis?** Rule 2 speaks of several researchers on one stream, but the drafts nest analyses under one's own streams and give the states to the stream.
   - *Recommendation:* at first nobody but the stream's owner. Later, a member whom the owner has invited by name, which keeps rule 4 intact. What is published is an analysis: a research page shows one analysis with its rules, and carries the stream's name, place and credit. A stream then has the states that concern its connection (draft, active, paused, suspended), an analysis those that concern visibility (private, published, withdrawn). Publishing an analysis of someone else's stream needs that owner's consent.
4. **Who builds the engine step, and in which order?** The maintainer's current thinking: the control API is first written as a spec here, and the engine work then happens in a session in the listen repository, so that two sessions never edit that repository at once.
   - *Recommendation:* agreed, with one addition. The spec should be read against the engine's code by the engine session before it counts as stable, since that is where it will meet the existing wiring. Changes found there come back as edits to the spec in this repository.

### Carried over

5. Which comes first for the first researchers: the member area or token access?
   - *Recommendation:* the member area, kept narrow: login, registering one's own stream, the probe, one private analysis page and the filter form. The data-stream token follows directly, MCP later.
6. Does the Workbench run on the same server and domain as the Observatory, or separately? The server is answered in part under [Dependencies](#dependencies): always on the engine process's host, and on the Observatory's host only when streams are shared. The domain is open.
   - *Recommendation:* a name of its own, so that cookies, the privacy notice and the content rules of the two sites stay apart.
7. Data stream: SSE, WebSocket, or both?
   - *Recommendation:* SSE only at first, see [Processes](#processes).
8. MCP: stay with a process reached over SSH, or offer an HTTP endpoint with a token? Only the second works for researchers without a shell account.
   - *Recommendation:* the HTTP endpoint, in the Workbench process.
9. May a researcher see or reuse another researcher's filters and motifs?
10. Does audio leave the Workbench at all (relay to the browser, captures), or analysis data only? This is a question for each stream's operator as much as for the design.
11. How many analyses may run on one stream, and what does one cost? (One listener with one analysis costs about 5 % of a core and 25 to 30 MB today. How much of that is the decode and how much the analysis has not been measured.)
12. Who reviews what becomes public?

### Gaps found in the review

13. What does a private analysis page show? The Observatory's live page is made for an audience. A researcher probably needs a spectrogram, the list of events, the motifs and an export. This is most of the work of the member area and is not described anywhere.
14. How is a token passed to the data stream? A request header works for programs. A token in the address would end up in logs.
15. What is a "project"? The table at the top says "one per researcher or project"; nothing else defines it.
16. Saving a rule change starts a fresh motif memory. With a form this happens at every correction. Should there be a trial mode that does not touch the memory, or a warning before saving?
17. How are the two repositories kept in step? A mechanism is proposed under [Staying in step](#staying-in-step).

Further gaps that concern accounts, streams and limits are listed in [members-and-streams.md](members-and-streams.md).
