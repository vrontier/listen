# Listening Observatory Workbench

**Listening Observatory Workbench (LOW)** is the research companion to [Listening Observatory](https://github.com/vrontier/listen): tools for researchers to explore live audio streams with their own filters, motifs and AI-assisted analysis.

The Observatory is a small, curated, public exhibit: a handful of streams, each analysed one way, with no accounts. The Workbench is where individual exploration happens. A researcher brings a stream, decides how it is analysed, and gets the results as pages, as a data stream, or through an AI assistant.

## Status

Design only. Nothing in this repository runs yet. The specs in [`docs/`](docs/) record what has been decided, what is proposed and what is still open.

## What it will offer

- **Public presentation of research:** pages that show ongoing work to visitors.
- **A member area for researchers:** usable without programming.
- **Streams:** select and gather streams to work on.
- **Filters and motifs:** define what the analysis leaves out and what it looks for.
- **A data stream by token:** analysis data over Server-Sent Events and/or WebSocket.
- **An MCP interface by token:** for AI-supported research.

## How it relates to Listening Observatory

```text
                 ┌──► Listening Observatory   curated public exhibit, no accounts
listener ────────┤
(shared engine)  └──► Workbench               accounts, tokens, own streams, own analyses
```

Both projects run on the same listener, the Go service in the Observatory's repository that decodes a stream and analyses it. The Workbench adds accounts, tokens and configuration on top; it does not fork the analysis.

Four rules shape the design (details in [docs/design.md](docs/design.md)):

1. **One shared engine.** Analysis code lives in the listener and is used by both projects.
2. **One connection per stream.** However many researchers work on a stream, its operator sees one connection. Each researcher's filters and motifs run as an additional analysis on the same decoded audio.
3. **The Observatory's filters never apply here.** A filter is a property of one analysis, not of the stream. The Workbench works on raw audio or on its own analysis, with its own rules or none.
4. **Researchers bring their own streams.** A stream is added by the person who runs it, or with the explicit permission of its operator.

## Documents

| Document | Content |
|---|---|
| [docs/design.md](docs/design.md) | Goals, the split from the Observatory, the design rules, features, order of work, open questions |
| [docs/analyses-and-filters.md](docs/analyses-and-filters.md) | One decode with several analyses; filter rules; what is and is not filtered; motifs |
| [docs/members-and-streams.md](docs/members-and-streams.md) | Accounts, registering a stream, states of a stream, limits, rights and privacy |

## Licence

MIT, like Listening Observatory. See [LICENSE](LICENSE). Audio remains with its sources and is never stored in this repository.
