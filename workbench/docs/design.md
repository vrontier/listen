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

## Features

**Decided** as the scope. The detail of each is **proposed** or **open** as noted.

| Feature | What it means | State of the detail |
|---|---|---|
| Public presentation of research | Pages that show ongoing work: the stream, the researcher's analysis, their notes | open: page design, who may publish what |
| Member area | Log in, manage streams, filters, motifs and tokens through forms | proposed in [members-and-streams.md](members-and-streams.md) |
| Streams | Register a stream, probe it, run it on demand | proposed, same document |
| Filters and motifs | Define what an analysis ignores and what it looks for | filters: rule syntax exists in the listener; motifs: drafted as "defined motifs" in the Observatory's spec |
| Data stream by token | The analysis events of one's own analyses over SSE and/or WebSocket | open: which transport, token format, limits |
| MCP by token | The same data and the motif memory for AI assistants | open: transport and authentication |

What exists today that these build on:

- The listener serves its analysis as a WebSocket event stream (`ws/live`) and a small HTTP API, both without authentication, on a local port behind a web server.
- A read-only MCP server (`listen-mcp`) runs next to the listeners and is reached over SSH. It has no tokens; access is whoever has the SSH key.
- Filter rules exist as the listener's `-ignore` option.
- One listener process runs exactly one analysis of one stream.

## What the engine must learn first

**Decided** as a dependency: rule 2 and rule 3 together need "one decode, several analyses" in the listener. Today the only ways to get a second analysis of a stream are to read the first one's (filtered) events or to start a second listener, which opens a second connection to the operator. Neither is acceptable for research use.

The same step is the "hub" of the earlier user-streams design: one long-running process that adds and removes streams and analyses without a root step on the server.

## Order of work

The order is **open**. Two views are on record:

- The maintainer expects the member area to be needed early, because the researchers are not all programmers. A token and a WebSocket URL are of little use to someone without a client.
- The earlier proposal was to start with tokens on the existing listeners, as the smallest step from what runs today.

A **proposed** order that respects the first view:

| Step | Content | Depends on |
|---|---|---|
| 1 | Listener: one decode, several analyses; add and remove them at run time | — |
| 2 | Catalogue and accounts: login by email link, own streams, private pages | 1; privacy notice and legal notice |
| 3 | Filters per analysis, set in a form; the page shows which rules are applied | 2 |
| 4 | Tokens: data stream and MCP for one's own analyses | 2 |
| 5 | Defined motifs per analysis; captures and trial runs | the listener's defined-motif work |
| 6 | Public research pages | 2, and a decision on publishing |

## Open questions

1. Which comes first for the first researchers: the member area or token access?
2. Does the Workbench run on the same server and domain as the Observatory, or separately?
3. Data stream: SSE, WebSocket, or both? SSE is simpler to consume and passes proxies easily; WebSocket is what the listener speaks today.
4. MCP: stay with a process reached over SSH, or offer an HTTP endpoint with a token? Only the second works for researchers without a shell account.
5. May a researcher see or reuse another researcher's filters and motifs?
6. How many analyses may run on one stream, and what does one cost? (One listener with one analysis costs about 5 % of a core and 25 to 30 MB today.)
7. Does audio leave the Workbench at all (relay to the browser, captures), or analysis data only? This is a question for each stream's operator as much as for the design.
8. Who reviews what becomes public?
