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
| Member | register streams and look after them as their steward, use every member's open streams in their own analyses, define own analyses, filters and motifs, create tokens, see own unpublished work, publish, withdraw, see own resource use |
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
   - "I run this stream or have the right to have it analysed" (**decided** as a requirement, see rule 4 in [design.md](design.md)). **Decided** (2026-10-15, question 12): if the member does not run the stream, they also give the operator's name or contact; the Workbench takes their word for the permission. **Built** (2026-10-15): two choices, "I run this stream myself" or "someone else runs it and has agreed", the second with a field for the operator's name and an email address or web page (3 to 200 characters). Shown on the source's page to its owner and in the master admin's list of streams; streams registered before show "not asked".
   - "Audio may be played in the browser", off by default. Audio is allowed (**decided**, 2026-10-14); the owner switches it on per stream.
2. **Probe.** On the Workbench's request the engine checks that the stream answers, delivers audio that decodes, and is not silent; it reports the sample rate, the level and the bit rate. The member sees the result at once. The sample rate becomes a property of the stream, the same for all its analyses (see [analyses-and-filters.md](analyses-and-filters.md)). A stream whose probe fails is not saved.
3. **Draft first** (until 2026-10-18; since then a new stream is active). **Decided** (2026-10-18): a new stream is open to every member at once, unless its steward ticks "only me for now" (see [Sources as a commons](#sources-as-a-commons)); before, it was visible only to its owner and the members they invited. It connects only while an analysis using it is in a run or a preview (decided 2026-10-15, see [design.md](design.md#runs)).
4. **No analysis is made.** **Decided** (2026-10-14): registering a source creates no analysis. The source's page offers "New analysis with this source"; see [design.md](design.md#analyses-and-sources).
5. **Afterwards.** The owner can edit the texts, move the stream to production, pause or delete it. The engine reports a stream that has been offline for 24 hours; the Workbench pauses it and tells the owner.

**Decided** (2026-10-14): a stream address exists once. If a second member registers an address that is already a stream, no second stream is made; the stream is shared (see [Sources as a commons](#sources-as-a-commons)). **Built** (2026-10-18): the second member is shown the existing source with a link, and can use it right away; if its steward keeps it private for now, they are told who registered it and that it appears once it is opened. Recognising two spellings of one address is the Workbench's task. **Decided** (2026-10-15): the obvious cases are normalised when a stream is registered (`http` and `https`, the host's case, a default port, a trailing slash); redirects are left for later. **Built** (2026-10-15): the address is stored with scheme and host in lower case, without a default port, a trailing slash or a fragment; one that exists over the other of `http` and `https` counts as the same stream. Addresses with a user name and password are refused. Streams registered before are not rewritten.

## States

### Of a stream

**Decided** (2026-10-15): a stream connects only while an analysis using it is in a run or a preview ([design.md](design.md#runs)). This replaces the rule of 2026-10-14 (draft on demand, production possibly always on); "always on" is removed (decided 2026-10-15). **Decided** (2026-10-18): draft and production, which connected alike from then on, are one state, active; streams in either became active, and the buttons "Put in service" and "Back to draft" are gone. Only a state other than active is shown as a tag.

```text
active ──► paused ──► active
active, paused ──► retired ──► active   (the steward or the master admin)
any state ──► suspended                 (master admin)
```

| State | Connected | Who sees it |
|---|---|---|
| Active | while an analysis using it is in a run or a preview | every member, or only its steward while private; published analyses are public |
| Paused | no | as when active; they see the last state |
| Retired | no | as when active; no new analysis can choose it, analyses that use it keep their history (**built** 2026-10-18) |
| Suspended | no | nobody; the steward sees the reason |

- **Decided:** once a stream is connected, it goes offline only when all its consumers have stopped. A consumer is a page or a token reading one of its analyses, a trial run, or an analysis in a run.
- **Decided:** a stream always shows who is connected to it. **Proposed:** its page lists, live, each analysis on it with its member's display name, whether it is in a run, and how many pages and tokens are reading. Everyone who may see the stream sees this list. The privacy notice has to say that display names are shown to invited members.
- **Proposed**, a reading of the answer above that the maintainer should confirm: the owner can still pause the stream while others are consumers, because the owner holds the operator's permission. The other members are told. Without this, a stream whose operator withdraws permission could not be stopped by its owner.
- **Proposed:** starting a run is checked against the caps (see [Resources and caps](#resources-and-caps)). A long run is for a stream the member runs or whose operator has agreed to continuous listening.
- A stream that connects only for runs and previews builds a motif memory with gaps. Every analysis page states the mode and which hours were actually heard, so that such a memory is not misread.

### Of an analysis

**Decided** (2026-10-14): publication belongs to an analysis, not to a stream, since analyses are individual and a stream is shared (see question 21 in [design.md](design.md#questions-and-answers)). An analysis has 1 to n sources ([design.md](design.md#analyses-and-sources)).

| State | Who sees it |
|---|---|
| Private | its member, and invited members if its member shares it |
| Published | everyone, at `/research/<slug>` |
| Withdrawn | as private; the public page answers that the work was withdrawn |

- **Decided:** publishing takes effect at once, without review.
- **Built** (2026-10-15): publishing can be switched off for the whole Workbench (`LOW_PUBLISHING=on` turns it on); off, the analysis page shows the Publish button disabled, nothing new can be published, and what is published stays and can be withdrawn. It is off on the intranet until the public area is ready (maintainer, 2026-10-15).
- Publishing asks once more for the rights attestation and shows the page as visitors will see it. A research page carries the name, place and credit of each of its sources.
- **Decided** (2026-10-18, replacing the proposal that the owner consents once per member): publishing an analysis that uses a source someone else registered needs no one's agreement. The operator agreed to analysis on this Workbench by its members, and the research page credits the source, its credit line and who registered it.
- Withdrawing takes effect at once.
- **Built** against the stand-in (2026-10-15), **proposed** in every detail:
  - **Publishing.** The analysis page has a section "Publication". "Publish…" shows the page as visitors will see it, says what they will and will not see, and asks for the rights once more (the confirmation is stored with the time in the audit trail) and whether to offer the data sets of finished runs. The page is public at once at `/research/<slug>`; the slug is made from the title the first time and kept.
  - **What visitors see.** The title and description, the member's display name, each source's name, place, description, credit and who registered it if it is someone else, the settings and filter rules in words, the live view (spectrograms, events, motifs and the comparison) while the analysis is in a run, and the finished runs, with their files when offered. Not shown: stream addresses, coordinates (also no map), email addresses. `/research` lists everything published; "Research" is in the menu for everyone, but only once something is published (proposed, 2026-10-15: an empty list in the menu only puzzles).
  - **Others' sources.** Built 2026-10-15 as a per-member "May publish" on the source's page, with withdrawal when the owner no longer agreed; **removed** on 2026-10-18 with the commons. A steward can no longer make a source private while another member's analysis uses it, so nothing published has to be withdrawn for that reason.
  - **Withdrawn.** The member withdraws on the analysis page; the public address then answers `410` with "This work was withdrawn on …". Publishing again uses the same address.
  - **Reports.** Every public page has a report form (message, optional contact; a hidden field and a limit of five reports an hour per address, fifty in all, keep robots out). Each report is mailed to the master admins and listed on the admin page, where they close it. The admin page also lists what is published, and the master admin can withdraw an analysis with a reason, which is mailed to its member.
- **Decided** (2026-10-15): public pages show a source's coordinates rounded to one decimal (about 10 km), with the map; the exact place stays with the member.
- **Decided** (2026-10-15): at most 50 visitors read one published analysis live at once; the master admin can change the number.
- **Built** (2026-10-15), **proposed** in its detail: a public page shows "near 50.1, 10.5"; the comparison's map on it uses the rounded places, and so do the engine's answers passed to visitors and a published `dataset.json` (rounded as it is sent; the member's own copy keeps the exact places). Visitors are counted by address, so one visitor with several live views open counts once; one turned away sees the page, its runs and data sets, with a note to come back later. The address is the one the local web server saw (the last entry of `X-Forwarded-For`, trusted only from the local web server), which also makes the report limit count visitors rather than the web server.
- **Open:** a privacy notice and legal notice before anything is public beyond the intranet (see [Rights and privacy](#rights-and-privacy)).
- The Workbench process checks who is asking, on every data path: the session of a logged-in member, or a token. The engine is reachable on a local socket only and checks nothing.

## Sources as a commons

**Decided** (2026-10-18, the maintainer, after the go-live; replaces the sharing by address of 2026-10-14 and 2026-10-15): sources are registered once and used by every member. `listen-ingest` exists so that a station is connected to once however many work on it; sharing by address made it hard for researchers to work on the same sources. Analyses stay individual. **Built** the same day:

- **Every member may use every open source** in their own analyses, without asking. Sources lists the member's own ("Registered by you") and every other member's open ones ("Registered by other members"), the new-analysis form offers them all, and so does the MCP tool `list_sources` (with `steward` for another member's).
- **The steward** is the member who registered the source. They keep its description, credit, place, audio switch and spectrogram view, hold the operator's agreement, and set its mode: draft, in service, paused, retired. The master admin can do all of it on every source. The steward no longer decides who uses the source.
- **Only me for now.** At registration a steward may tick that the source is theirs alone for now, for example while they find out whether the stream is reliable; it is off by default. Other members do not see a private source; registering its address again tells them who registered it. The steward opens it on its page at any time. A source can be made private again only while no other member's analysis uses it.
- **Retire instead of delete.** A source that other members' analyses use cannot be deleted, since that would take it out of their work. Its steward retires it instead: nothing connects, no new analysis can choose it, and the analyses that use it keep it and their history. It can be brought back. A source only its steward uses can still be deleted.
- **The operator's agreement** is now asked for analysis on this Workbench by its members, not by one member: "Someone else runs it, and they have explicitly agreed to its analysis on this Workbench by its members."
- **Costs** are counted as before: a source's connection in equal parts against the members whose analyses run on it.
- **The Observatory's stations.** With every station read through `listen-ingest`, a station the Observatory listens to is not connected to a second time by the Workbench. A station whose operator agreed to the Workbench is registered once like any other source; the three VLF stations, with the maintainer as steward. `LOW_RESERVED_INPUTS` now lists only streams whose operators have not agreed.
- The table `stream_shares` stays in the catalogue, unused.

### Sharing by address (2026-10-14 to 2026-10-18, superseded)

Streams were shared, analyses individual: the owner shared a stream on its page with a member by their address, the member used it read-only in their own analyses, and the owner could stop sharing at any time; publishing an analysis on another member's source needed that owner's agreement per member. Removed on 2026-10-18 (see above); the details are in the history of this file.

### The shared library

- **Decided:** there is a shared library for all members. **Proposed:** it is a part of the Workbench, not a git repository. A member puts a set of filter rules into it with a name and a note; others take a copy into their own analysis. A copy, not a link, so that a later change in the library never changes someone's analysis or starts a new memory behind their back. Motif definitions, captures and detections can go into the library in the same way once they exist (members' question 7).
- **Built** against the stand-in (2026-10-15), **proposed** in its detail:
  - "Library" in the menu lists every set: name, note, the member who put it in, when, the rate of the source it came from, how often it was taken, and its rules in words.
  - A member puts a set in from a source of their own analysis ("Put these rules in the library" under its filter rules): the rules of the saved revision, with a name and a note. Their display name is shown with it.
  - Taking a set copies its rules into a source of one's own analysis, added to its rules (without duplicates) or in their place. The engine checks them at that source's rate first; the result is the analysis's next revision, so a new memory begins as with any change of rules. Not during a run, which is fixed. When the set was made at another rate, the member is told that tolerances left empty are a number of bins, whose width differs.
  - The member who put a set in, or the master admin, can take it out of the library. Copies already taken stay. Putting in, taking and taking out are in the audit trail.
- **Decided** (2026-10-16): the library is taken out of the Workbench for the time being: no menu entry, no pages, nothing on the analysis page. Sets already put in stay in the catalogue, so that it can come back. The code is in the history before this decision. **Open:** when it comes back.

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
- **Disk.** The quota counts the memories and history of the member's analyses and their captures. At 80 % the member is warned. At 100 % nothing new that writes to disk can start (a rule change starts a new memory, a capture). Running analyses continue and keep writing until their run ends (**decided** 2026-10-15); at 110 % a run ends early, with a note in its data set and a mail. Nothing is deleted automatically; the member deletes old memories on the analysis page.
- **The view.** `/account/usage` shows the member's running streams and analyses with CPU, RAM, bandwidth and disk, against their cap, over the last day and month. `/manage` shows the same for the whole server.

**Built** against the stand-in (2026-10-15), **proposed** in every detail:

- **Attribution.** The engine's usage report (every 10 s) is attributed as the decided rule says: each engine analysis, with its preview and trial, to its member; each stream's connection and decode in equal parts to the members with something running on it, or to the stream's owner when nothing is. The engine as a whole is counted for the server.
- **History.** The attributed use is averaged per minute and kept for 32 days, per member and for the engine.
- **Caps.** The master admin sets the server cap, a member cap for every member, and, where needed, a cap of a member's own; each as CPU cores and memory in MB, empty meaning none. The admin page shows each member's use now, their data sets and their cap.
- **Reservation.** Starting a run, a preview or a trial is refused when the member's use now plus an estimate of what it adds would go over the member's cap, or the engine's use plus the estimate over the server cap; the refusal says by how much. The estimate, until the engine measures per analysis before it starts: one analysis per running source at 1 % of a core and 8 MB, plus the connection of each source not yet connected at 1.5 % and 25 MB, both at 22 050 Hz and scaled with the rate. **Decided** (2026-10-15): these estimates stand until the engine measures. The comparison's cost is not in the engine's report yet and not counted.
- **Disk.** Data sets and the engine's memories of the member's analyses count against the 0.5 GB quota (memories **decided** 2026-10-15). The engine reports a memory's size only while the analysis is in it, during a run; the Workbench keeps the size last reported, every 30 s, so that it counts between runs too. A run is refused while data sets and memories fill the quota; previews and trials, which write nothing, are not. From 80 % the Results page says so. During a run, the analysis page offers to delete a memory other than the one in use.
- **The view.** **Decided** (2026-10-16): the usage page becomes "Results" (`/results`), which shows a member their results, the data sets of their runs, and how their disk space is used, as a bar with the values, with a way to download and delete their files; the usage figures stay on it. **Built** against the stand-in (2026-10-16), **proposed** in its detail: the bar splits data sets and memories, with used, free and the percentage; each data set is listed like a finished run, with the storage it takes and a link "(Results)" to its run's page, where it downloads as one zip (dataset.json, events.csv, comparison.csv when there is one) or file by file; ticked data sets are deleted together, the runs stay listed; memories are listed by analysis and source with their size, cannot be downloaded, and are deleted on the analysis page during a run as before; below, use now against the cap, what runs and what it costs, and CPU per hour over the last 24 hours (labelled in hours back from now) and per day over the last 30 (labelled with the day and, below it, the month as a three-letter abbreviation). `/account/usage` redirects to `/results`. The admin page shows the engine's CPU per hour.
- **Not stopped.** What runs is never stopped when its measured use rises above a cap.

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

**Built** against the stand-in (2026-10-15), **proposed** in every detail:

- **The AI pages.** **Decided** (2026-10-16): "Tokens" and "MCP" become one entry "AI" in the menu, holding the tokens (access and permissions), the MCP documentation and the activity. **Proposed** (suggested 2026-10-16, built against the stand-in): a sub-menu Overview · Access · MCP · Activity (`/ai`, `/ai/access`, `/ai/mcp`, `/ai/activity`). The overview says what an assistant can do (read; change only with control; never register, share, publish or delete), how to give a researcher's assistant access in four steps (make a token, connect, ask, keep an eye on it), that programs use the same tokens through the data stream, and where the member stands. The earlier addresses forward: `/account/tokens` to Access, `/account/mcp` to MCP (to Activity when it carries a filter), `/account/activity` to Activity.
- **The page.** "Access" under AI (`/ai/access`, before 2026-10-16 "Tokens", `/account/tokens`): a member makes a token with a name and what it is for (data stream, MCP, or both), sees it once, and later sees each token's first characters, what it is for, when it was made and last used, and can revoke it. At most 20 valid tokens per member. Making and revoking are in the audit trail.
- **Activity.** **Decided** (2026-10-16): every access with a member's tokens is tracked, on a page of its own. **Built** against the stand-in, **proposed** in its detail: "Activity" under AI (`/ai/activity`; before, part of "MCP", `/account/mcp`) shows, per token, the accesses in the last 24 hours, the failed ones, those in the last 30 days, the data streams open now and the last access; below, every access in a scrolling box, each over two lines (when, token and client, MCP or data stream, the call, the result, how long, how much; below it, the arguments and the message), the newest 500 per page, filtered in the browser by token, MCP or data stream, call, result (ok, failed, streaming) and free text, and sorted by any column; the choice is kept in the address (**proposed**, built 2026-10-16). A note holds the time, the token, the program as it names itself (MCP `clientInfo`, otherwise the User-Agent), the MCP method or tool with its arguments or the data stream's path and query, the result (ok, error with its message, refused when the token is not for MCP or the data stream, streaming while live events are sent, cut off when the Workbench restarted meanwhile), how long it took and how much went back. Notes are kept 30 days. The token and the other headers are never noted. A request with an unknown or revoked token cannot be put to a member and is not noted.
- **Guide for assistants.** **Decided** (2026-10-16): the MCP page also lists every tool and helps less capable models use them. **Built** against the stand-in, **proposed** in its detail: "MCP" under AI (`/ai/mcp`) shows each tool with its arguments (required first) and an example call using the member's own ids; a plain-text guide (what the Workbench is, the order of the tools: list_analyses first, live tools only in a run or preview, data sets through get_dataset before files, what to do with an error) to copy into a session; and how to connect (endpoint, `claude mcp add`, `.mcp.json`). The same guide, without the member's ids, is the server's `instructions` in its answer to `initialize`, so that every client receives it.
- **The token.** `low_` and 43 random characters; stored as a SHA-256 hash. A token in the address (`?token=`) is refused with a note to use the header. A revoked token, or one of a suspended member, is refused.
- **It reads, it does not start** (unless allowed control, see below, **decided** 2026-10-16). A token reads the member's own analyses only; without control it never starts, stops or changes anything. So reading never connects a source: this settles how tokens fit with "a source connects only for runs and previews". Live data and events are there while the analysis is in a run or a preview; otherwise the answer is `409 not_running`. A data set can be read at any time.
- **Addresses on the production server.** **Decided** (2026-10-16): the data stream is at `/data/v1/…` and the master admin's page at `/manage`, because the production web server refuses any path that begins with `admin`, `api`, `rest`, `v1` or `v2`. The earlier `/api/v1/…` and `/admin` forward (308 and 301) for the intranet's sake. **Decided** (2026-10-16): the IONOS admin is asked to exempt `/mcp` and `/data/v1/` from the server's bot filter (not from its rate limits), since both answer only to a valid token and their clients are programs; not yet answered.
- **What it reads** (scope "data stream"):
  - `GET /data/v1/analyses` and `/data/v1/analyses/<id>`: title, description, revision, whether it is in a run or a preview, its sources with place, coordinates, rate, steward (`steward`, the member who registered it) and settings, and what is compared.
  - `GET /data/v1/analyses/<id>/events`: the live events as Server-Sent Events. Each event keeps the engine's type, and its data is `{"source": <id>, "data": <the engine's event>}`, or `{"comparison": true, "data": …}` for the comparison. `?types=` limits the types (comma-separated names such as `event.transient`; a name not formed like an engine type is answered with 400 `bad_types`), `?source=` the sources (an id that is not one of the analysis's sources: 400 `bad_source`). With `?types=`, the first line names the types kept: `: connected to <title>; only event.transient`. When the run or preview ends, an `end` event says so and the stream closes. At most four such streams per token at once. A token reading counts in the source's list of who is connected.
  - `GET /data/v1/analyses/<id>/sources/<source>/state` (also `motifs`, `motifs/<id>`, `memories`, `history/events`, `history/features`): the engine's data, during a run or a preview.
  - `GET /data/v1/runs` and `GET /data/v1/runs/<id>/<file>`: the runs and their data sets' files, uncompressed.
- **MCP** (scope "MCP"): `POST /mcp`, the streamable HTTP transport of MCP in its simplest form. Every request gets one JSON answer; the server sends nothing of its own accord, so `GET /mcp` answers 405. Protocol versions 2025-06-18, 2025-03-26 and 2024-11-05. Requests with an `Origin` of another site are refused. Nine read-only tools: `list_analyses`, `get_analysis`, `get_state`, `get_motifs`, `get_history_events`, `get_comparison`, `list_runs`, `get_dataset`, `read_dataset_file` (500 lines per call, the CSV header with each). None of these starts, stops or changes anything; the tools that do are listed under control below.
- **For sessions with a language model** (**proposed**, built against the stand-in 2026-10-16 after a test from Claude Code): `list_analyses` gives the short form (ids, titles, states, sources with name, place and state); `get_analysis` keeps the full detail. Two more tools: `summarize_analysis` says in a few hundred bytes where an analysis stands (in a run: per source its connection, events by type in the last minutes and in the whole run, the last event and absence; in a preview: what each source hears; otherwise its last data set) and which call comes next; `get_events_since` gives what a run recorded after a cursor, compact and oldest first across the sources, with counts per source and type and a new cursor, so a session asks "what is new" without holding a stream open (`limit` 0: counts only, the cursor at the end). The cursor names the run and, per source, the lines of the Workbench's own recording already seen; it works only during a run, and after the run's end it points to the data set. `get_dataset` gives each file's size uncompressed and its number of lines and rounds seconds; `read_dataset_file` ends with a line saying which lines of how many and the next offset. Errors name the id that was not found and the call that gives the ids, and say why a data set cannot be read (still recording, being put together, failed, deleted, none, no comparison with one source). In a new data set's `events.csv` the absences stand at their time among the events, not after them.
- **Resources and prompts** (**proposed**, built 2026-10-16): resources `low://guide` and, per finished run, `low://runs/<id>/summary` and `low://runs/<id>/<file>` (files up to 1 MB; larger ones through `read_dataset_file`); prompts `summarize_run`, `watch_analysis`, `compare_sources`, which word the task and name the tools in order. Reading a resource and getting a prompt are noted like tool calls.
- **Control by token.** **Decided** (2026-10-16): a member may allow a token to change things, per token; an assistant may then make and edit analyses and start and stop previews and runs; each such token has limits of its own, set by the member, including none; the member gets a mail whenever a token starts a run. **Built** against the stand-in, **proposed** in its detail: a third permission "control" next to "data stream" and "MCP" on the Tokens page, only together with MCP, changeable later on the same page; limits per token: the longest run in hours (empty: runs until stopped allowed too), runs started a day, the token's runs at once, previews a day, changes to analyses a day (offered: 24, 5, 1, 20, 50; an empty field is no limit). Through MCP only; the data stream stays read-only. Tools, shown only to a token with control: `create_analysis`, `update_analysis` (title, description), `set_sources`, `update_source_settings` (only the fields given change, `null` returns one to the default, `rules` replace all rules; the engine checks first; a new revision), `start_preview`, `stop_preview`, `start_run` (`hours`, `offline_stop_min`, 15 by default), `stop_run`; and for every MCP token the read tool `list_sources`. They go through the same checks as the pages: caps, disk quota, sharing, nothing of an analysis changes during a run. A token never registers, shares, publishes or deletes. A call over a limit or without control is answered "Refused: …" with the limit, and noted as refused. Every change is in the audit trail as "by token: <tool>" with the token, a run started by a token names it on the run's page and in the run lists, and the MCP page marks changes and filters by them. Counting: runs from the runs table; previews and changes from the token's successful calls in the last 24 hours (an unchanged save counts). The guide for assistants gets a section on changing things when the token has control.
- **Open:** which assistants can send a static `Authorization` header. **Assumed**, not tested: Claude Code (`claude mcp add --transport http … --header "Authorization: Bearer low_…"`) and editors configured by a JSON file can; the custom connectors of the claude.ai and ChatGPT apps expect OAuth and cannot. If those are wanted, the Workbench needs OAuth 2.1 as the MCP specification describes it (question 14).

## Failures and the spectrogram view

- **A failed analysis.** **Decided** (2026-10-16, with the engine's owner): an analysis can fail in the engine on its own, while its stream and the other analyses go on. **Built** against the stand-in, **proposed** in its detail: the analysis's page shows "Failed" and the engine's reason on that source, with "Start it again", which puts the analysis into the engine anew; during a run the member gets a mail naming the run and the source. Every new start of the engine, or of the Workbench's connection to it, also puts every analysis again.
- **A source's spectrogram view.** **Decided** (2026-10-16): a stream has a view of its own for the spectrogram, unfiltered, on a linear or log scale over a range of its own (for VLF radio 0 to 16 kHz), computed once per stream. **Built** against the stand-in, **proposed** in its detail: the source's owner sets it on the source's page (scale, lowest and highest Hz, bands; empty for the engine's defaults); the engine checks it, and a refused view is not kept; the spectrogram of every analysis of the source then draws the view's bands on its scale, with the axis to match. Without a view the spectrogram shows each analysis's bands, as before. The landscape keeps the analysis's bands.

## Account

**Decided** (2026-10-16): a member changes their own display name and email address on a page of their own; the address, being the login, changes only once the new address has confirmed it. **Built** against the stand-in, **proposed** in its detail:

- **The page.** `/account`, reached by the member's name at the bottom of the sidebar: the name, the address, and the theme (the same choice as in the sidebar).
- **Name.** 2 to 60 characters, changed at once; in the audit trail.
- **Address.** The member gives a new address; a link goes there, valid 24 hours, and only the newest request counts. Until it is opened nothing changes, and the member logs in with the old address. Opening the link shows a button first, so that a mail program's preview does not use it up. Confirming makes the new address the login, sends the old address a notice, ends every session and unused login link of the member, and logs in the browser that confirmed. The member can cancel a waiting change.
- **Refusals.** An address that another account or an open invitation has is refused without saying whose ("cannot be used for your account"), on the request and again on confirmation. At most 5 requests an hour per member, counted from the audit trail.
- **Audit.** Each request, each change of name and of address, with before and after.
- Tokens are not affected; they belong to the account, not to the address.

## Language-model keys

**Decided** (2026-10-16): the language-model interpretation of analyses goes into the backlog. The keys page is switched off: no menu entry, `/account/keys` not found, unless the Workbench runs with `LOW_LLM_KEYS=on` (not set on the intranet). Keys saved before stay sealed in the catalogue. What follows stands for when it comes back.

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

**Built** (2026-10-15), **proposed** in every detail; the interpretation itself waits for the engine:

- `/account/keys`: one form per provider (Anthropic, OpenAI). A key is checked before it is saved, by listing the provider's models (`GET /v1/models`), a call that is not billed; a refusal (401, 403) or no answer means it is not saved. A rate limit (429) counts as accepted, since the provider checks the key first. Redirects are not followed, so the key goes nowhere else.
- Before any call, the key's shape is checked: length, no spaces, the provider's prefix (`sk-ant-` for Anthropic, `sk-` for OpenAI), an Anthropic key entered under OpenAI, and Anthropic admin keys, which are refused because the Workbench needs no more than an ordinary key.
- A member can cause at most ten checks an hour, so that the page cannot be used to try out keys found elsewhere.
- Sealed with AES-256-GCM under `LOW_KEYS_SECRET` (32 bytes, base64) from the server's settings file, bound to the member and provider, so a sealed key copied to another member's row does not open. The install script makes the secret once. Without it, keys cannot be opened: it belongs with the backups of the catalogue, but kept apart from them. Without the secret set, the page says so and saves nothing.
- The page shows the last four characters, when the key was saved and last accepted, and whether a later check was refused. "Check again" asks the provider anew; "delete" removes it at once, and the page reminds the member to delete it at the provider too.
- Saving, replacing and deleting are in the audit trail, with the provider and the last four characters only. The key never appears in a log, a page, a token's or MCP's answers.
- **Not built:** passing the key to the engine with an analysis's settings, the choice of model, the count of calls per month, and telling the member when the engine's calls are refused. These need the engine's interpretation ([control-api.md](control-api.md#reserved)).

## Filtering and limits

| Rule | Proposal |
|---|---|
| URL | `http` and `https` only; the decoder is restricted to those protocols |
| Addresses the Observatory listens to | **decided** (2026-10-16): refused at registration and kept out of the engine, from a list set on the server (`LOW_RESERVED_INPUTS`, a file of hosts or addresses, not in the repository), until the stream's operator has agreed to the Workbench. Since `listen-ingest` holds the one connection to each station, the list is about consent only: an agreed station attaches to the existing connection ([control-api.md](control-api.md#which-addresses-the-engine-connects-to)) |
| Requests to internal addresses | the stream's address must not be loopback, private, link-local or the server's own; checked again on every redirect and reconnect. **Needs an engine change:** today ffmpeg opens the connection, follows redirects and reconnects by itself, so only the address as registered can be checked. The engine has to open the HTTP connection itself and hand the bytes to ffmpeg. Until then sign-up is by invitation only, which is **decided**. |
| Content | an audio content type, decodable, at most 512 kbit/s |
| Per member | **decided:** no limit on the number of streams; a cap on CPU and RAM, and a disk quota of 0.5 GB, see [Resources and caps](#resources-and-caps) |
| Per stream | a cap on analyses, and on rules and definitions per analysis |
| Overall | **decided:** a cap on CPU and RAM for the server, set by the master admin |
| Texts | length limits; links only in the credit field |
| Abuse | a "report" link on every public page; the master admin can suspend a stream or an account |

No user-written code or expressions run on the server: filters and motif definitions are parameters for detectors that the listener provides. The engine checks them before the Workbench saves them.

## Rights and privacy

- The owner's attestation is stored with a timestamp, and for a stream the owner does not run, with the operator's name or contact; it is where a takedown request starts.
- An account stores an email address and a display name.
- An account may store a language-model API key per provider, encrypted.
- Before sign-up opens beyond the intranet, the site needs a privacy notice and a legal notice covering accounts, the email login, tokens, language-model keys and what is sent to those providers, logs, the audit trail and the display names shown to invited members.

## Pages

| Path | Content |
|---|---|
| `/login` | email link |
| `/` | for members, Home (**decided** 2026-10-16, the landing page after login): the way through the Workbench step by step (source, analysis and preview, run and trial, results, tokens and MCP, publishing when it is on), with links and where the member stands at each step; for visitors, the front page |
| `/sources` | own sources, with state |
| `/sources/new`, `/sources/<slug>` | register and edit, invite members, move to production, pause, delete; who is connected; new analysis with this source |
| `/analyses` | own analyses with their sources |
| `/analyses/new`, `/analyses/<id>` | start and stop a run, or a ten-minute preview; from two sources the comparison (during runs); per source together: spectrogram, events and motifs (during a run or a preview), settings and filter rules (fixed during a run), memories (listed during runs); the choice of sources and comparison; title and description; runs with their data sets, revisions |
| `/runs` | runs of the member's analyses: running now (stop), start a run, finished runs (delete), each with a link to its results |
| `/runs/<id>` | one run: when, revisions, its data set (what it holds, the files singly at their uncompressed size or together as one zip, delete) |
| `/results` | own data sets (download, delete), disk space as a bar, memories, CPU, RAM and bandwidth against the caps |
| `/account` | own name, email address (changed once the new one confirms it) and theme |
| `/ai` | AI overview: what an assistant can do, how to give it access step by step, where the member stands |
| `/ai/access` | create tokens, change what they may do and their limits, revoke them |
| `/ai/mcp` | the MCP tools with examples, a guide for assistants, how to connect |
| `/ai/activity` | every access with the member's tokens over 30 days |
| `/account/keys` | own API keys for the language-model interpretation: switched off, in the backlog (decided 2026-10-16) |
| `/library` | shared filter rule sets: taken out for the time being (decided 2026-10-16) |
| `/research` | everything published |
| `/research/<slug>` | a published research page |
| `/manage` | invitations, caps, usage of the server, suspensions, audit log |

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
   - **Decided:** yes, through a shared library. See [The shared library](#the-shared-library).
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
    - **Decided** (2026-10-15): take their word. A member who does not run the stream themselves gives the operator's name or contact, which is kept with the attestation and its time. With sign-up by invitation, the master admin knows every member; the contact is what a takedown request starts from. This has to be looked at again if sign-up ever opens beyond invitation.
