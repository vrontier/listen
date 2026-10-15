# Members and streams

Status: draft (2026-10-13), with the maintainer's answers of 2026-10-14. Nothing here is built yet.

This document carries over two drafts written for Listening Observatory before the projects were split: its `docs/user-streams-design.md` (2026-10-02) and Part C of `docs/profiles-and-community-spec.md` (2026-10-10). They are adapted here to the Workbench. Everything in it is a proposal unless marked as decided.

## Goal

A researcher can create an account, register a stream, decide how it is analysed, look at the results privately, and choose to publish. This needs no mail to the maintainer, no root step on the server and no programming.

Not in scope: uploading recordings, comments or social features.

## Roles

| Role | Can |
|---|---|
| Visitor | see published research pages |
| Member | register and configure streams, invite other members to work on them, define own analyses, filters and motifs, create tokens, see own unpublished work, publish, withdraw, see own resource use |
| Master admin | invite members, set the server's caps and each member's cap, suspend streams and accounts, see the audit log, maintain built-in settings |

- **Decided** (2026-10-14): the member is the one who sets up streams. There are no organisation accounts that register a feed of many streams.
- **Decided:** nothing is reviewed before it becomes public. The member decides.
- **Decided:** sign-up is by invitation. **Proposed:** the master admin sends the invitations.

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
- **Catalogue:** a small database with users, streams, analyses, rule and profile revisions, tokens, resource records and an audit log. It belongs to the Workbench process alone; the engine does not read it. The earlier drafts had the hub read the catalogue.
- **Accounts:** login by email link, so there are no passwords to store.

## Registering a stream

1. **Form.** The stream URL, a name, the place, the time zone, a description, a credit line and link. Two checkboxes:
   - "I run this stream or have the right to have it analysed" (**decided** as a requirement, see rule 4 in [design.md](design.md)).
   - "Audio may be played in the browser", off by default. Audio is allowed (**decided**, 2026-10-14); the owner switches it on per stream.
2. **Probe.** On the Workbench's request the engine checks that the stream answers, delivers audio that decodes, and is not silent; it reports the sample rate, the level and the bit rate. The member sees the result at once. The sample rate becomes a property of the stream, the same for all its analyses (see [analyses-and-filters.md](analyses-and-filters.md)). A stream whose probe fails is not saved.
3. **Draft first.** A new stream is in draft: it runs only while someone watches, a token reads or an analysis using it is in a run (decided 2026-10-15, see [design.md](design.md#runs)), and is visible only to its owner and the members they invite.
4. **No analysis is made.** **Decided** (2026-10-14): registering a source creates no analysis. The source's page offers "New analysis with this source"; see [design.md](design.md#analyses-and-sources).
5. **Afterwards.** The owner can edit the texts, move the stream to production, pause or delete it. The engine reports a stream that has been offline for 24 hours; the Workbench pauses it and tells the owner.

**Decided** (2026-10-14): a stream address exists once. If a second member registers an address that is already a stream, no second stream is made; the stream is shared (see [Sharing](#sharing)). **Proposed:** the second member is shown that the stream exists and who owns it, and can ask the owner to be invited. Recognising two spellings of one address (redirects, `http` and `https`, a trailing slash) is the Workbench's task and is **open** in its detail.

## States

### Of a stream

**Decided** (2026-10-14): a stream in draft runs only while a member watches or a token reads; a stream in production may run always on. **Decided** (2026-10-15): in either state it also runs while an analysis using it is in a run ([design.md](design.md#runs)). The rest of this table is **proposed**.

```text
draft ──► production
  ▲  │        │
  │  ▼        ▼
  paused ◄────┘

any state ──► suspended (master admin)
```

| State | Connected | Who sees it |
|---|---|---|
| Draft | while at least one consumer is attached or an analysis using it is in a run | the owner and invited members |
| Production | always, if the owner chooses "always on"; otherwise as in draft | the owner and invited members; published analyses are public |
| Paused | no | the owner and invited members see the last state |
| Suspended | no | nobody; the owner sees the reason |

- **Decided:** once a stream is connected, it goes offline only when all its consumers have stopped. A consumer is a page or a token reading one of its analyses, a trial run, or an analysis that runs always on.
- **Decided:** a stream always shows who is connected to it. **Proposed:** its page lists, live, each analysis on it with its member's display name, whether it runs always on, and how many pages and tokens are reading. Everyone who may see the stream sees this list. The privacy notice has to say that display names are shown to invited members.
- **Proposed**, a reading of the answer above that the maintainer should confirm: the owner can still pause the stream while others are consumers, because the owner holds the operator's permission. The other members are told. Without this, a stream whose operator withdraws permission could not be stopped by its owner.
- **Proposed:** moving to production and switching on "always on" are checked against the caps (see [Resources and caps](#resources-and-caps)). Always on is for a stream the member runs or whose operator has agreed to continuous listening.
- A stream in draft builds a motif memory with gaps. Every analysis page states the mode and which hours were actually heard, so that such a memory is not misread.

### Of an analysis

**Decided** (2026-10-14): publication belongs to an analysis, not to a stream, since analyses are individual and a stream is shared (see question 21 in [design.md](design.md#questions-and-answers)). An analysis has 1 to n sources ([design.md](design.md#analyses-and-sources)).

| State | Who sees it |
|---|---|
| Private | its member, and invited members if its member shares it |
| Published | everyone, at `/research/<slug>` |
| Withdrawn | as private; the public page answers that the work was withdrawn |

- **Decided:** publishing takes effect at once, without review.
- Publishing asks once more for the rights attestation and shows the page as visitors will see it. A research page carries the name, place and credit of each of its sources.
- **Proposed:** publishing an analysis that uses a source someone else owns needs that owner's consent, given once per member.
- Withdrawing takes effect at once.
- The Workbench process checks who is asking, on every data path: the session of a logged-in member, or a token. The engine is reachable on a local socket only and checks nothing.

## Sharing

**Decided** (2026-10-14): streams are shared, analyses are individual. There are no projects. A member defines streams and filters and may let others work on them.

- **Proposed:** a stream's owner invites members by name. An invited member may add their own analyses to the stream and sees its page and its list of consumers. They cannot change the stream's settings or texts.
- **Proposed:** each analysis belongs to one member. Others see it only if it is published or its member shares it with the stream's invited members.
- **Decided:** there is a shared library for all members. **Proposed:** it is a part of the Workbench, not a git repository. A member puts a set of filter rules into it with a name and a note; others take a copy into their own analysis. A copy, not a link, so that a later change in the library never changes someone's analysis or starts a new memory behind their back. Motif definitions, captures and detections can go into the library in the same way once they exist (members' question 7).

## Resources and caps

**Decided** (2026-10-14):

- The Workbench monitors how many streams are running and what each costs in CPU, RAM and bandwidth.
- The cost of each analysis is measured and accounted against CPU and RAM.
- Each member sees their own use, as a visualisation.
- There is a cap for the whole server and a cap per member, both set by the master admin.
- When a cap is reached, no stream or analysis can be added. There is no cap on the number of streams as such.
- Each member has a disk quota, 0.5 GB to start with.

**Proposed**, how:

- **What is counted.** A stream's connection and decode (CPU and memory of its ffmpeg process, incoming bandwidth) and each analysis (CPU time and memory). Audio played to browsers adds an encoder and outgoing bandwidth per listener. The engine measures; the Workbench records and adds up (see [control-api.md](control-api.md#usage)).
- **Whose cap it counts against.** Nothing here is about money; the costs are counted against the members' CPU and RAM caps. Each analysis counts against its own member's cap. **Decided** (2026-10-14): the connection and decode of a shared stream is split equally among the members who have an analysis running on it. While only one member has one, that member carries all of it. The server's cap counts the decode once.
- **Reservation, not hindsight.** Starting something reserves its measured cost (until measured: a fixed estimate). The Workbench refuses a start whose reservation would exceed a cap. This includes a page opened on an idle stream in draft: if the server is full, the page says so instead of connecting.
- **Running work is not stopped** when the measured cost of something already running rises above a cap. The member and the master admin are told.
- **Disk.** The quota counts the memories and history of the member's analyses and their captures. At 80 % the member is warned. At 100 % nothing new that writes to disk can start (a rule change starts a new memory, a capture). Running analyses continue; whether they keep writing history is **open**. Nothing is deleted automatically; the member deletes old memories on the analysis page.
- **The view.** `/account/usage` shows the member's running streams and analyses with CPU, RAM, bandwidth and disk, against their cap, over the last day and month. `/admin` shows the same for the whole server.

### Measurement limits

**Assumed** from the engine's code (checked 2026-10-14), to be confirmed by the engine session:

- ffmpeg runs as a child process per stream, so its CPU and memory can be read exactly.
- The analyses all run inside the one engine process. Their CPU time can be measured by timing each analysis's work. Their memory cannot be read separately in Go; it can only be estimated from what they hold (spectra, buffers, the motif memory). The engine process's total is exact.
- Incoming bandwidth is exact only once the engine opens the stream's HTTP connection itself (see [design.md](design.md#a-third-change-for-safety)). Until then it is taken from the bit rate the probe measured.

## Audit trail

**Decided** (2026-10-14): creation, change and deletion of streams, their settings and their filters are recorded.

**Proposed:**

- Recorded also: analyses, invitations, publishing and withdrawing, tokens created and revoked, suspensions, caps set.
- Each entry: time, acting member, object, what changed (old and new values).
- A member sees the entries about their own streams and analyses; the master admin sees all.
- Kept as long as the object exists and one year after its deletion. The privacy notice names this.

## Tokens

A member creates tokens for programs and AI assistants. A token:

- belongs to one member and gives access to that member's analyses only;
- can be limited to reading the data stream, to MCP, or both;
- can be revoked at any time, and shows when it was last used;
- is shown once when created and stored only as a hash.

**Decided:** Server-Sent Events for the data stream, and an HTTP endpoint in the Workbench process for MCP.

**Proposed**, as most private APIs do it (question 14 in [design.md](design.md#questions-and-answers)):

- A program sends the token in the request header `Authorization: Bearer <token>`. Never in the address, where it would end up in logs and browser histories.
- The token carries a recognisable prefix, for example `low_`, so that a token pasted into a public repository can be found by secret scanning.
- A browser page uses the session cookie instead.

## Language-model keys

**Decided** (2026-10-14): a member who wants the language-model interpretation of their analyses registers their own API key, for the OpenAI API or the Anthropic API. The use is billed to the member's account with that provider; the Workbench handles no money.

**Proposed:**

- Without a key there is no interpretation. Everything else of an analysis works without one.
- One key per provider and member, entered on `/account/keys`. After saving, only its last four characters are shown again. The member can replace or delete it at any time.
- Before saving, the Workbench makes one small call to the provider to check that the key works.
- In the catalogue the key is stored encrypted. The encryption key is part of the server's configuration, not of the catalogue, so a copy of the database alone does not reveal it.
- A key is used only for the analyses of the member it belongs to, also on shared streams.
- The Workbench passes the key to the engine with the analysis's settings. The engine holds it in memory only: it never writes it to disk or to its logs, and never returns it in a response (see [control-api.md](control-api.md#reserved)).
- The interpretation runs only while someone watches, as today, so that a key is not used up by an empty page. The page shows how many calls were made with the member's key this month.
- Deleting a key, or the provider refusing it, stops the interpretation and tells the member.

## Filtering and limits

| Rule | Proposal |
|---|---|
| URL | `http` and `https` only; the decoder is restricted to those protocols |
| Requests to internal addresses | the stream's address must not be loopback, private, link-local or the server's own; checked again on every redirect and reconnect. **Needs an engine change:** today ffmpeg opens the connection, follows redirects and reconnects by itself, so only the address as registered can be checked. The engine has to open the HTTP connection itself and hand the bytes to ffmpeg. Until then sign-up is by invitation only, which is **decided**. |
| Content | an audio content type, decodable, at most 512 kbit/s |
| Per member | **decided:** no limit on the number of streams; a cap on CPU and RAM, and a disk quota of 0.5 GB, see [Resources and caps](#resources-and-caps) |
| Per stream | a cap on analyses, and on rules and definitions per analysis |
| Overall | **decided:** a cap on CPU and RAM for the server, set by the master admin |
| Texts | length limits; links only in the credit field |
| Abuse | a "report" link on every public page; the master admin can suspend a stream or an account |

No user-written code or expressions run on the server: filters and motif definitions are parameters for detectors that the listener provides. The engine checks them before the Workbench saves them.

## Rights and privacy

- The owner's attestation is stored with a timestamp and is where a takedown request starts.
- An account stores an email address and a display name.
- An account may store a language-model API key per provider, encrypted.
- Before sign-up opens beyond the intranet, the site needs a privacy notice and a legal notice covering accounts, the email login, tokens, language-model keys and what is sent to those providers, logs, the audit trail and the display names shown to invited members.

## Pages

| Path | Content |
|---|---|
| `/login` | email link |
| `/sources` | own sources, with state |
| `/sources/new`, `/sources/<slug>` | register and edit, invite members, move to production, pause, delete; who is connected; new analysis with this source |
| `/analyses` | own analyses with their sources |
| `/analyses/new`, `/analyses/<id>` | title, description and sources; start and stop a run; per source: spectrogram, events, motifs, settings and filter rules, memories; from two sources the comparison; export, runs with their export, trial, revisions |
| `/runs` | runs of the member's analyses: running now (stop), start a run, finished runs with their export |
| `/account/usage` | own CPU, RAM, bandwidth and disk against the caps |
| `/account/tokens` | create and revoke tokens |
| `/account/keys` | own API keys for the language-model interpretation |
| `/library` | shared filter rule sets |
| `/research/<slug>` | a published research page |
| `/admin` | invitations, caps, usage of the server, suspensions, audit log |

## Questions and answers

Answers are the maintainer's of 2026-10-14 and are **decided**. Recommendations are the reviewer's and are **proposed**.

1. Publishing: at once, or after an admin's review?
   - **Decided:** at once, without review.
2. Who reviews: only the maintainer, or a small group?
   - **Decided:** nobody; there is no review (question 1). A "review" meant a person checking a stream or page before it becomes visible to the public. The safeguards that remain are the report link and suspension by the master admin.
3. Audio relay for member streams: allowed, or analysis data only at first?
   - **Decided:** allowed. **Proposed** detail in question 10 of [design.md](design.md#questions-and-answers).
4. Should the language-model interpretation run for member analyses?
   - **Decided:** yes, through the OpenAI API and the Anthropic API.
   - **Verified:** the engine's narrator speaks the OpenAI chat-completions format only, with a bearer key, and runs only while someone watches. **Assumed:** the Anthropic API can be reached through its OpenAI-compatible endpoint; whether it accepts every field the narrator sends is unchecked. Otherwise the engine needs a second client. **Decided:** each member registers their own API key; see [Language-model keys](#language-model-keys).
5. Are 3 streams per member and the overall cap right to start with?
   - **Decided:** no limit on the number of streams; only compute and RAM are capped.
6. Organisations with many receivers: an account that registers a feed?
   - **Decided:** no. The member is the one who sets up streams.
7. May a member share a capture, its detections, or a set of rules with others?
   - **Decided:** yes, through a shared library. See [Sharing](#sharing).
8. Is sign-up open, or by invitation?
   - **Decided:** by invitation.
9. What happens when two members register the same stream address?
   - **Decided:** streams are shared, analyses are individual. See [Registering a stream](#registering-a-stream).
10. How many analyses may one member run in total?
    - **Decided:** as many as their CPU and RAM cap allows. When a cap is reached, nothing more can be added.
11. How long are an analysis's memory and history kept, and how much disk may a member use?
    - **Decided:** a disk quota per member, 0.5 GB to start with. **Proposed:** see [Resources and caps](#resources-and-caps).
12. What is checked about "I run this stream or have the right to have it analysed"?
    - The question, put differently: when a member ticks the box for a stream they do not run themselves, does the Workbench take their word for it, or ask for something that shows the operator agreed?
    - *Recommendation:* take their word, and in that case ask for the operator's name or contact and keep it with the attestation. With sign-up by invitation, the master admin knows every member; the contact is what a takedown request starts from. This is still **open**.
