# Members and streams

Status: draft (2026-10-13). Nothing here is built yet.

This document carries over two drafts written for Listening Observatory before the projects were split: its `docs/user-streams-design.md` (2026-10-02) and Part C of `docs/profiles-and-community-spec.md` (2026-10-10). They are adapted here to the Workbench. Everything in it is a proposal unless marked as decided.

## Goal

A researcher can create an account, register a stream, decide how it is analysed, look at the results privately, and choose to publish. This needs no mail to the maintainer, no root step on the server and no programming.

Not in scope: uploading recordings, comments or social features.

## Roles

| Role | Can |
|---|---|
| Visitor | see published research pages |
| Member | register own streams, define analyses, filters and motifs, create tokens, see own unpublished work, publish, withdraw |
| Admin | review, suspend streams and accounts, maintain built-in settings |

## Architecture

**Proposed.** The split into two processes and its reasons are in [design.md](design.md#processes). In short:

```text
browser ──► web server ──► Workbench process ──control API──► engine process ──► streams
                           (pages, accounts, forms,            (all streams and
                            tokens, data stream, MCP)           analyses, memory)
                                 │
                                 ▼
                             catalogue
```

- **Engine process:** one long-running service built from the listener's packages, in the listen repository. It keeps one connection per active stream and runs the analyses on it. It knows nothing about members. The earlier drafts call it "the hub"; that name is already taken inside the engine.
- **Workbench process:** the pages, the accounts and everything that checks access. Adding, pausing or removing a stream or an analysis is a change in the catalogue that the Workbench passes to the engine through the control API within seconds. After a restart of either side the Workbench sends the engine the full wanted state again.
- **Catalogue:** a small database with users, streams, analyses, rule and profile revisions, tokens and an audit log. It belongs to the Workbench process alone; the engine does not read it. The earlier drafts had the hub read the catalogue.
- **Accounts:** login by email link, so there are no passwords to store.

## Registering a stream

1. **Form.** The stream URL, a name, the place, the time zone, a description, a credit line and link. Two checkboxes:
   - "I run this stream or have the right to have it analysed" (**decided** as a requirement, see rule 4 in [design.md](design.md)).
   - "Audio may be played in the browser", off by default.
2. **Probe.** On the Workbench's request the engine checks that the stream answers, delivers audio that decodes, and is not silent; it reports the sample rate and level. The member sees the result at once. The sample rate becomes a property of the stream, the same for all its analyses (see [analyses-and-filters.md](analyses-and-filters.md)).
3. **Private first.** The stream runs on demand and is visible only to its owner, logged in. Whether on demand is the right default is open, see [design.md](design.md#open-questions), question 2.
4. **Afterwards.** The owner can edit the texts, pause or delete the stream. The engine reports a stream that has been offline for 24 hours; the Workbench pauses it and tells the owner.

## States of a stream

```text
draft ──► private ──► published
            ▲  │          │
            │  ▼          ▼
          paused      withdrawn (back to private)

any state ──► suspended (admin)
```

| State | Analysed | Who sees it | Listed |
|---|---|---|---|
| Draft | no | nobody; the probe has not passed yet | no |
| Private | yes, on demand | the owner, logged in | no |
| Published | yes | everyone | yes |
| Paused | no | the owner sees the last state | no |
| Suspended | no | nobody; the owner sees the reason | no |

- A private stream connects only while its owner is looking at it or a token is reading it, so unpublished work costs almost nothing and the operator's server is not held open. The price is a motif memory with gaps: learned motifs need continuous listening. This is open, see [design.md](design.md#open-questions), question 2.
- The Workbench process checks who is asking, on every data path: the session of a logged-in member, or a token. The engine is reachable on a local socket only and checks nothing. The earlier drafts had the site issue a short-lived signed token that the hub verified; with all outside traffic passing through the Workbench process this is not needed.
- Publishing asks once more for the rights attestation and shows the page as visitors will see it.
- Withdrawing takes effect at once.
- The states above are given to the stream. Whether it is the stream or an analysis that is published, and who may add an analysis to someone else's stream, is open, see [design.md](design.md#open-questions), question 3.

## Tokens

A member creates tokens for programs and AI assistants. A token:

- belongs to one member and gives access to that member's analyses only;
- can be limited to reading the data stream, to MCP, or both;
- can be revoked at any time, and shows when it was last used;
- is shown once when created and stored only as a hash.

Server-Sent Events are proposed as the transport of the data stream, and an HTTP endpoint in the Workbench process for MCP. The token's format and how it is passed are open; see [design.md](design.md).

## Filtering and limits

| Rule | Proposal |
|---|---|
| URL | `http` and `https` only; the decoder is restricted to those protocols |
| Requests to internal addresses | the stream's address must not be loopback, private, link-local or the server's own; checked again on every redirect and reconnect. **Needs an engine change:** today ffmpeg opens the connection, follows redirects and reconnects by itself, so only the address as registered can be checked. The engine has to open the HTTP connection itself and hand the bytes to ffmpeg. Proposed: until then, sign-up is by invitation only (question 8 below). |
| Content | an audio content type, decodable, at most 512 kbit/s |
| Per member | 3 streams, raisable per account |
| Per stream | a cap on analyses, and on rules and definitions per analysis |
| Overall | a cap on running streams, set from measured cost |
| Texts | length limits; links only in the credit field |
| Abuse | a "report" link on every public page; an admin can suspend a stream or an account |

No user-written code or expressions run on the server: filters and motif definitions are parameters for detectors that the listener provides. The engine checks them before the Workbench saves them.

## Rights and privacy

- The owner's attestation is stored with a timestamp and is where a takedown request starts.
- An account stores an email address and a display name.
- Before any sign-up opens, the site needs a privacy notice and a legal notice covering accounts, the email login, tokens and logs.

## Pages

| Path | Content |
|---|---|
| `/login` | email link |
| `/account` | own streams, analyses and tokens, with state |
| `/account/streams/new`, `/account/streams/<slug>` | register and edit, publish, pause, withdraw, delete |
| `/account/streams/<slug>/analyses/<id>` | settings, filter rules, motif definitions, revisions |
| `/account/tokens` | create and revoke tokens |
| `/research/<slug>` | a published research page |
| `/admin` | review queue, suspensions, audit log |

## Open questions

1. Publishing: at once, or after an admin's review? Earlier proposal: review for a member's first stream, none afterwards, with suspension as the safeguard.
2. Who reviews: only the maintainer, or a small group?
3. Audio relay for member streams: allowed, or analysis data only at first?
4. Should the language-model interpretation run for member analyses?
5. Are 3 streams per member and the overall cap right to start with?
6. Organisations with many receivers: an account that registers a feed (a list of its streams) was drafted for the Observatory. Does it belong in the Workbench's first version?
7. May a member share a capture, its detections, or a set of rules with others?

Gaps found in the review of 2026-10-13. The recommendations are the reviewer's and are **proposed**.

8. Is sign-up open, or by invitation?
   - *Recommendation:* by invitation at first. It keeps moderation small and is required anyway until the engine can check a stream's address on every redirect and reconnect (see the limits above).
9. What happens when two members register the same stream address? Rule 2 allows one connection, so the second registration cannot become a second stream. Who is then the owner, and how are two addresses recognised as the same stream (redirects, different spellings)?
   - *Recommendation:* the first registration owns the stream; a second one is refused with a note that the stream exists. This follows from the recommendation on question 3 in [design.md](design.md#open-questions).
10. How many analyses may one member run in total? The draft caps streams per member and analyses per stream, not analyses per member.
11. How long are an analysis's memory and history kept, and how much disk may a member use? Each change of rules starts a new memory, so old ones pile up.
12. What is checked about "I run this stream or have the right to have it analysed"? Rule 4 asks for an operator's explicit yes; the form records the member's word for it.
   - *Recommendation:* for a stream the member does not run, ask for the operator's contact or a copy of the agreement, kept with the attestation.

The questions that shape the architecture (whose streams, on demand or continuous, what is published, who builds the engine step) are in [design.md](design.md#open-questions).
