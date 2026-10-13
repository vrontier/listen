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

```text
browser ──► web server ──► site (pages, accounts, forms)         ──► catalogue
                      └──► hub (all streams and analyses)        ◄── reads catalogue
```

- **Hub:** one long-running service built from the listener's packages. It reads the catalogue, keeps one connection per active stream and runs the analyses on it. Adding, pausing or removing a stream or an analysis is a catalogue change the hub picks up within seconds.
- **Catalogue:** a small database with users, streams, analyses, rule and profile revisions, tokens and an audit log.
- **Accounts:** login by email link, so there are no passwords to store.

## Registering a stream

1. **Form.** The stream URL, a name, the place, the time zone, a description, a credit line and link. Two checkboxes:
   - "I run this stream or have the right to have it analysed" (**decided** as a requirement, see rule 4 in [design.md](design.md)).
   - "Audio may be played in the browser", off by default.
2. **Probe.** The hub checks that the stream answers, delivers audio that decodes, and is not silent; it records the sample rate and level. The member sees the result at once.
3. **Private first.** The stream runs on demand and is visible only to its owner, logged in.
4. **Afterwards.** The owner can edit the texts, pause or delete the stream. The hub pauses a stream that has been offline for 24 hours and tells the owner.

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

- A private stream connects only while its owner is looking at it or a token is reading it, so unpublished work costs almost nothing and the operator's server is not held open.
- The hub checks who is asking. The site issues a short-lived signed token to the logged-in owner; the hub verifies it on every data path.
- Publishing asks once more for the rights attestation and shows the page as visitors will see it.
- Withdrawing takes effect at once.

## Tokens

A member creates tokens for programs and AI assistants. A token:

- belongs to one member and gives access to that member's analyses only;
- can be limited to reading the data stream, to MCP, or both;
- can be revoked at any time, and shows when it was last used;
- is shown once when created and stored only as a hash.

Transport and format are open; see [design.md](design.md).

## Filtering and limits

| Rule | Proposal |
|---|---|
| URL | `http` and `https` only; the decoder is restricted to those protocols |
| Requests to internal addresses | the stream's address must not be loopback, private, link-local or the server's own; checked again on every redirect and reconnect |
| Content | an audio content type, decodable, at most 512 kbit/s |
| Per member | 3 streams, raisable per account |
| Per stream | a cap on analyses, and on rules and definitions per analysis |
| Overall | a cap on running streams, set from measured cost |
| Texts | length limits; links only in the credit field |
| Abuse | a "report" link on every public page; an admin can suspend a stream or an account |

No user-written code or expressions run on the server: filters and motif definitions are parameters for detectors that the listener provides.

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
