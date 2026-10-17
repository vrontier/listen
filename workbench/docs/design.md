# Design: Listening Observatory Workbench

Status: draft (2026-10-13), with the maintainer's answers of 2026-10-14. Nothing here is built yet.

Each statement in this document is marked by where it stands: **decided** (agreed, build to it), **proposed** (a suggestion that has not been confirmed) or **open** (a question without an answer).

## Why a separate project

Listening Observatory began as one site that would grow a community section: accounts, own streams, own analysis settings. That would have put personal data, access tokens and legal obligations into what is otherwise a small public exhibit.

**Decided:** the two concerns are split.

| | Listening Observatory | Workbench |
|---|---|---|
| Purpose | show a few streams to the public | support individual research |
| Streams | curated by the maintainer | brought by researchers |
| Accounts | none | yes |
| Analysis per stream | one, configured by the maintainer | several; each belongs to one researcher |
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
- **`listen-ingest`** (**decided** 2026-10-16, the maintainer, on the engine owner's proposal) is the platform's only component that connects to stations: one small service per machine, in the listen repository, that holds the one connection to each station and passes its original bytes to the engine and to the Observatory's listeners on the same machine. "Read each source once, then distribute" therefore holds for the whole platform, not only inside one process, and rule 2 below is kept by the ingest rather than by people. Running on the intranet host since 2026-10-16; on the production host (ionos, where the Workbench's production will run beside the Observatory, decided 2026-10-16) after the intranet has been evaluated. See [control-api.md](control-api.md#which-addresses-the-engine-connects-to).
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

- Only if the Workbench is to analyse streams that the Observatory also analyses. Rule 2 then requires that one engine process holds the connection for both, so both run on the same host and use the same engine process. **Decided** (2026-10-14): not at first. The Workbench starts with members' own streams (question 1), so it needs no shared host.

**Consequence:** anyone running a Workbench fetches and builds from two repositories. Moving the engine into a repository of its own is a possible later step. It is not planned now.

**What exists today:** the engine process the Workbench needs is not built. Today's listener runs one analysis of one stream per process, is started with command-line flags, and has no interface for adding a stream or an analysis while it runs.

### Staying in step

**Decided** (2026-10-14, question 17). The two repositories change independently, so the Workbench must be able to tell whether the engine next to it fits.

- The control API (see [Processes](#processes)) has a version of its own with two numbers. The first is raised for a change that breaks existing callers, the second for an addition.
- The engine process reports this version and its own build version on the control API.
- The Workbench asks at start and whenever it reconnects. If the first number differs from the one it was built for, or the second is lower than it needs, it refuses to change anything in the engine, says so on its admin page and in its log, and keeps serving pages.
- This repository states the minimum engine version it needs, in one place (a line in the README or a small file), and the control API spec in `docs/` carries the API version it describes.
- The engine's repository tags the commits that change the control API version, so "minimum engine version" names something that can be checked out.

## Processes

**Proposed**, all of this section except where marked decided. It comes from a review of the engine's code on 2026-10-13.

```text
browser, programs, AI assistants
        │
        ▼
web server ──► Workbench process ──control API, local socket──► engine process ──► listen-ingest ──► stations
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

**Decided** (2026-10-14): Go, and the Go binary also renders the HTML pages. There is no PHP part. **Proposed:** pages rendered on the server with Go's templates, plain JavaScript, vendored dependencies and no build step for the front end.

The Observatory's site is vanilla PHP behind NGINX. The reasons that were given for Go:

- **Long-lived connections.** The data stream and the MCP endpoint are connections that stay open for minutes or hours and must be checked against a session or a token. Under PHP-FPM each of them occupies one worker for its whole life.
- **One place that checks access.** With PHP pages and a separate Go program for the long-lived connections, two programs would have to verify sessions and tokens, and two would write to the catalogue. One process is simpler to reason about and to secure.
- **One toolchain.** The engine is Go. The Workbench shares no code with it, but the same language, test tools and build serve both.

What speaks against it:

- Pages and forms are quicker to write in PHP, and the Observatory's site already has a contact form and mail setup to borrow from.
- The maintainer then keeps two kinds of site.

Deployment is not a reason either way. A Go binary needs a one-time root step on a server for its service unit, its user and its web server routes, just as a PHP site needs one for its pool and its routes.

### Server-Sent Events for the data stream

**Decided** (2026-10-14, question 7): SSE first. The reasons:

- The engine's event stream is one-way today, server to client, so SSE loses nothing against WebSocket.
- SSE is plain HTTP: it passes proxies, works with `curl`, and a program sends its token in a request header.
- A browser page uses the session cookie, since a browser's `EventSource` cannot set headers.
- Every event carries a sequence number, so a client can see that it missed something. Replaying missed events is not offered: the engine keeps no backlog.
- WebSocket is not offered at first. Audio is allowed (question 10); it gets a channel of its own and never travels in the data stream.

## Features

**Decided** as the scope. The detail of each is **proposed** or **open** as noted.

| Feature | What it means | State of the detail |
|---|---|---|
| Public presentation of research | Pages that show ongoing work: the stream, the researcher's analysis, their notes | decided: the member publishes at once, no review; an analysis is what is published (question 21); built against the stand-in (2026-10-15), detail proposed in [members-and-streams.md](members-and-streams.md#of-an-analysis); open: page design |
| Member area | Log in, manage streams, filters, motifs and tokens through forms | decided: comes first, kept narrow (question 5); an analysis page shows the spectrogram, the events, the motifs and an export (question 13); detail proposed in [members-and-streams.md](members-and-streams.md) |
| Sources | Register a stream, probe it, move it from draft to production, pause it, share it with invited members; see who is connected; it connects only for runs and previews | decided in outline (questions 1 to 3); detail proposed, same document. Called "Sources" in the menu (decided 2026-10-14) |
| Filters and motifs | Define what an analysis ignores and what it looks for; a shared library; a trial mode | filters: rule syntax exists in the listener; library and trial mode decided (questions 9, 16); motifs: drafted as "defined motifs" in the Observatory's spec |
| Data stream by token | The analysis events of one's own analyses | decided: SSE, follows the member area; proposed: token in the `Authorization` header (question 14); built against the stand-in (2026-10-15), see [members-and-streams.md](members-and-streams.md#tokens) |
| MCP by token | The same data and the motif memory for AI assistants | decided: an HTTP endpoint in the Workbench process, after the data stream; proposed: the same token header; built against the stand-in (2026-10-15) with nine read-only tools; proposed (2026-10-16): two more for language-model sessions (a summary and a "since" cursor), resources and prompts; decided (2026-10-16): control per token (making and editing analyses, previews and runs) with limits per token, built against the stand-in, see [members-and-streams.md](members-and-streams.md#tokens) |
| Analyses | A member's analysis of 1 to n sources: settings and filter rules per source; from two sources, the sources aligned on one time axis and compared (activity side by side, station clocks, impulses shared between stations) | decided, see [Analyses and sources](#analyses-and-sources); the comparison is proposed in [groups.md](groups.md) |

What exists today that these build on:

- The listener serves its analysis as a WebSocket event stream (`ws/live`) and a small HTTP API, both without authentication, on a local port behind a web server.
- A read-only MCP server (`listen-mcp`) runs next to the listeners and is reached over SSH. It has no tokens; access is whoever has the SSH key.
- Filter rules exist as the listener's `-ignore` option.
- One listener process runs exactly one analysis of one stream.
- ffmpeg opens the connection to the stream and reconnects by itself. The listener does not see the HTTP exchange.

## Analyses and sources

**Decided** (2026-10-14). An analysis is the member's own object, and it uses 1 to n sources. It is not an element of a source.

- **The menu** has "Sources" (the registered streams) and "Analyses", and since 2026-10-15 "Runs" (see [Runs](#runs)).
- **A source** is registered, probed, described, located, switched between draft and production, paused and deleted. Its page shows who is connected: which members' analyses use it and how many pages and tokens are reading. Registering a source creates no analysis; its page offers "New analysis with this source".
- **An analysis** has a name, a description and its sources. **Settings are kept per source inside the analysis:** range, sensitivity, filter rules and memory, because interference belongs to a receiver (one station's mains run at 50 Hz, another's at 60 Hz). A button copies one source's settings to all the others, for sources that share their interference.
- **One revision number covers the whole analysis:** its sources and all their settings together, so that a result can be traced to exactly one configuration.
- **From two sources on**, the analysis also compares them on one time axis: the analyses drafted in [groups.md](groups.md).
- **In the engine nothing changes.** The Workbench puts an analysis with n sources into the engine as n engine analyses, one per source with that source's settings, and from two sources on as one engine group over them. The control API keeps its terms: "analysis" there is the engine's analysis of one stream, "group" the engine's comparison of several.

This replaces the earlier drafts in which an analysis was made under a stream, and "groups" as a separate object in the Workbench.

## Runs

**Decided** (2026-10-15): a member starts and stops an analysis as a **run**, and each run produces a **data set**.

- **A source connects only while an analysis that uses it is in a run or a preview.** An open page alone no longer connects it. This replaces "a stream in draft runs only while a member watches or a token reads" (question 2, decided 2026-10-14). **Proposed** (2026-10-15) for tokens: a token reads what runs and never starts anything, so it never connects a source (since 2026-10-16 a token the member allows control may start previews and runs, which connect as the pages' do); outside a run or a preview its data stream answers that the analysis is not running ([members-and-streams.md](members-and-streams.md#tokens)).
- **The live view** of an analysis (spectrograms, events, motifs, comparison) is shown only during its run or a preview.
- **A source's state says how far along the source is** (draft, production, paused), not whether it runs.
- **Start and stop.** A run is started with a planned length or "until stopped", and can be stopped at any time.
- **A data set per run.** What a run finds is recorded while it runs and kept as a fixed data set when it ends. Stopping a run leads to its page with the data set; the analysis page and the Runs list link to it.
- **A preview** shows an analysis live for ten minutes and records nothing.
- **A run is fixed** (decided 2026-10-15): while it lasts, the analysis's sources and every setting that is part of a revision (range, sensitivity, filter rules, memory mode, the comparison) cannot be changed, so that a run and its data set hold exactly one configuration. Title and description stay editable; they are not part of a revision. To change the configuration, the member stops the run; the next run starts with the new revision. Tuning next to a running run is the trial mode's job (question 16).
- **The analysis page is grouped per source** (decided 2026-10-15): each source has its live view, its settings and filter rules, and its memories together; the choice of sources, the comparison and the save button follow below them.
- **A menu "Runs"** manages runs across analyses: what is running now, with a stop button; starting a run of any analysis; the finished runs with their data sets.

**Proposed** (built against the stand-in on 2026-10-15):

- **One run at a time** per analysis. The lengths offered are 1 hour, 6 hours, 24 hours (preselected), 3 days, 7 days and until stopped. Three days or more covers the premise test of [groups.md](groups.md#the-premise-test).
- **The analysis is in the engine only during a run.** The engine runs every analysis of a connected stream, so an analysis left in it would learn and store whenever anything connects the stream. Outside a run it is taken out without deleting its memories, and the next run continues them. Consequences: outside a run the settings show no "in use" values from the engine, the memories are listed only during a run, and the comparison exists only during a run.
- **The data set is recorded by the Workbench**, not read from the engine afterwards: one subscription per source (events without the frames and spectra, which come ten a second) and one for the comparison (activity per second, correlations), written to files as they arrive. This does not depend on the memory mode (a source on "learn but keep nothing" still has its events in the data set), covers every revision of the run, and survives engine restarts. After a Workbench restart the recording catches up from the engine's history and drops what it already has.
- **When the run ends** the recording is frozen, compressed, into three files: `dataset.json` (the run, the settings of every revision in force during it, each source's events and its motifs at the end, the comparison's correlations), `events.csv` (all sources, a column naming the source) and `comparison.csv` (a row per source and second of activity, a row per pair and minute of correlation). The CSV files are plain, with the header as their first line and nothing before it, so that spreadsheets read them; what the run was is in the JSON. The run's page shows what it holds (events per source and type, motifs, the last correlations) and offers the files uncompressed. The free-period export is gone; data sets replace it.
- **Data sets count against the member's disk quota** of 0.5 GB (decided 2026-10-14). The run page shows the use; a data set can be deleted. Not enforced yet. How large a day is has not been measured.
- **A finished run can be deleted** by its member (**decided** 2026-10-16), with its data set. **Built** against the stand-in, **proposed** in its detail: on the Runs page, finished runs are ticked and deleted together; a run still going on, or whose data set is still being put together, cannot be. A deleted run goes from every list, from its analysis and from what is published; the other runs keep their numbers; the change log keeps what it was.
- **Runs from before data sets** have none, and say so.
- **A preview** is a second, temporary engine analysis per source (`p-NNNN` next to `a-NNNN`) with the source's settings and the memory mode `ram`: it learns motifs in memory and forgets them, and the analysis's own memory is not touched. It ends after ten minutes or by the button; starting a run ends it. There is no comparison in a preview. This is the mechanism the trial mode needs too ([control-api.md](control-api.md#answers-to-the-open-points), point 3).
- **Previews and trials survive a restart of the Workbench** (**proposed**, built 2026-10-16): both are kept in the catalogue as well as in memory and taken up again when the Workbench starts, so a deploy no longer ends them; one past its end meanwhile is ended at once. While a preview lasts, its streams stay connected even without a page open (the engine's `continuous`), so a preview started through MCP listens too.
- **"Always on" is removed** (decided 2026-10-15): with sources connecting only for runs and previews it had no effect left. A run "until stopped" does what it did.
- **The lock is enforced by the Workbench:** during a run the settings form is shown read-only, and a save is refused with the reason. Runs made before the lock may span revisions; their list entry shows the range ("rev-1 to rev-3"), and every event carries its revision.
- **Absences are recorded.** A source can still drop out during a run: its station goes offline, or its owner pauses it. The recording keeps every change of each source's state that the engine reports, and the data set lists the periods of a second or more in which a source was not connected, with what the engine reported meanwhile (connecting, removed, …): in the JSON per source, as rows `source.absent` in `events.csv`, and as "not heard" on the run's page. The comparison already marks absent seconds per source.
- **A paused source does not run**, even in a run; the analysis page says so. Pausing stays the owner's way to stop a stream for everyone.
- **A run ends at its planned end on its own**, checked every 30 seconds; the audit trail records start, stop, end and the deletion of a data set.
- **In the engine** a run needs nothing new: the analysis is put in, and the stream is put with `mode` `continuous` instead of `on_demand` ([control-api.md](control-api.md#streams)). When the run ends, the analysis is taken out once the data set is made, and the stream goes back to `on_demand`.
- **Caps.** A run reserves its sources' and analyses' cost for its length, as in [Resources and caps](members-and-streams.md#resources-and-caps). Starting a run whose reservation would exceed a cap is refused. Not enforced yet, like the caps themselves.
- **Decided** (2026-10-15): a run ends by itself when its sources stop sending, because the analysis is off from then on. A switch in the run's setup: "stop the run when a source has sent nothing for N minutes", N 15 by default. Independently of the switch, a run whose sources have all sent nothing for 15 minutes always stops. The data set notes why the run ended. **Proposed:** the switch is on when the setup opens.
- **Decided** (2026-10-15): the member gets one short mail when a run ends. **Proposed:** not when they stopped it themselves.
- **Decided** (2026-10-15): when the disk quota is full, running analyses keep writing until their run ends; at 110 % of the quota a run ends early, with a note in its data set and the mail.
- **Built** (2026-10-15), **proposed** in its detail: the switch and its minutes (1 to 1440) sit next to the run's length on the analysis page and the Runs page, and the run page shows them. Every 30 s the Workbench reads each source's last audio from the engine; time before the run's start does not count, so a source that never connects stops the run after the minutes set. Nothing is checked while the engine itself is unreachable. A run that ends by itself keeps why (`planned`, `offline`, `silent`, `disk`) and a note in words, shown on its pages and written into its data set (`ended_because`, `end_note`, `offline_stop_min`). The mail goes once per run, also for a planned end; runs ended before this was built are not mailed about.

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
- **Measuring.** The engine reports the CPU, memory and bandwidth of each stream and analysis, for the Workbench's accounting (decided, question 11; see [members-and-streams.md](members-and-streams.md#resources-and-caps)).

Piece B is needed as soon as one stream has a second analysis: a member comparing two settings on their own stream, a second member on the same stream, or a trial run next to the analysis it tries a change for. With shared streams and the trial mode decided, the Workbench needs piece B early.

### A third change, for safety

The limits in [members-and-streams.md](members-and-streams.md) ask that a stream's address is checked again on every redirect and reconnect. That cannot be done while ffmpeg owns the connection. The engine would have to open the HTTP connection itself and hand the bytes to ffmpeg. The Observatory's draft on station time asks for the same change for another reason. It is needed before strangers can register stream addresses, not before the first private use.

### For groups: station time, impulse times, the group service

**Proposed** (2026-10-14). Analysing several streams together ([groups.md](groups.md)) needs three more things of the engine, in this order:

- **The group service:** aligning the events of several analyses on one time axis, and the group analyses. With wall time it can run `activity`, accurate to seconds.
- **Station time:** reading the timestamps some streams carry in their Ogg container, so that events carry the time at the station rather than the time they reached the engine. It builds on the third change above: the engine reads the bytes itself and passes them to ffmpeg. The listen repository's draft also records an unexplained lag of motif times after a long outage, to be found as part of this.
- **Impulse times:** a new low-level output of an analysis, timed to well under a millisecond, which `clock` and `coincidence` work on.

The listen repository's draft makes the timing analyses conditional on a premise test with `activity`; [groups.md](groups.md#the-premise-test) takes this over.

## Look and feel

**Decided** (2026-10-16): the pages take the design language of a designer's proposal ("Field Station", kept outside this repository), fitted to the Workbench: a light, quiet workspace around dark listening instruments. Its sections are the Workbench's own (Home, Sources, Analyses, Runs, Results, AI); an analysis's title is never a section.

- **Night theme.** **Decided** (2026-10-16): from the start, with the same colour roles, not an inversion. It follows the system unless the member chooses light or night in the sidebar (**proposed**: kept in the browser).
- **Fonts.** **Decided** (2026-10-16): Source Sans 3 for the text, Source Serif 4 for page titles, IBM Plex Mono for values, times and code; served by the Workbench, under the SIL Open Font License. They replace JetBrains Mono.
- **Navigation.** **Decided** (2026-10-16): a sidebar; on narrow screens a bar with a menu button. **Proposed**: AI's pages (Access, MCP, Activity) appear beneath AI in the sidebar while one is open, in place of a sub-menu on the page.
- **Home.** **Decided** (2026-10-16): stays the step-by-step page, in the new look; the proposal's dashboard is not taken over.
- **Colours.** **Proposed**: the proposal's tokens (warm canvas, white surfaces, teal for actions and what works, copper as an accent, a dark instrument surface), with night values of the same roles; every text pair at least 4.5:1, checked for both themes.
- **Status words.** **Proposed** (2026-10-16): one word per meaning on every page, always with its colour: teal working, blue in progress, amber needs a look, red failed, grey at rest. Sources: Draft, In service, Paused, Suspended. Connections: Not connected, Connecting, Live, Waiting for audio, Reconnecting, Not reachable. Analyses: Running, Preview, Idle; Private, Published, Withdrawn. Data sets: Recording, Finishing, Ready, Failed, Deleted, No data set. Accesses: OK, Failed, Refused, Streaming, Cut off. The stored values, and what MCP and the data stream return, do not change. Not built yet: "Stale since …" for a live view that hears nothing while its connection says live, and the words for how a run ended.
- **Instruments.** **Proposed** (2026-10-16), built against the stand-in: what a source hears, and the comparison's lanes, are drawn on the dark instrument surface, with axes and units, a legend for the spectrogram's colours, a value readout under the pointer or with the arrow keys once focused, and a Pause that freezes the drawing only and says so ("the analysis goes on"). A short text under each source says what the newest second shows, for everyone and for screen readers.
- **Spectrogram.** **Decided** (2026-10-16): drawn like the usual scientific spectrogram, after [this reference](images/spectrogram-reference.png): the "jet" colour scale from dark blue (level 0) through cyan, yellow and red to dark red (level 1), a colour bar with ticks beside it, frequency upwards on a log axis with ticks ("f, Hz"), and seconds before now along the bottom with the time of the newest column in UTC. **Proposed**: peaks after the filter rules marked in white; time not yet heard stays the instrument's dark background, not level 0.
- **Spectral landscape.** **Proposed** (2026-10-16), built against the stand-in: a ridgeline view of the last 60 seconds of `signal.spectrum` per source, one line a second (the mean of its spectra, with every peak seen in it), flat rather than in perspective, frequency on a log axis, height as relative level (the engine sends 0 to 1, not dB) on a fixed scale; spectrum lines in the instrument's colour, older ones fainter, detected peaks in copper; seconds without audio stay empty and are labelled, and "No audio since …" shows when nothing arrives for 3 s; with reduced motion it redraws every 5 s. The default view per source; the spectrogram is the other view, and the choice is kept in the browser. The proposal's copper-to-teal colouring by frequency is not taken over: it repeats the axis and reads as intensity.

## Order of work

**Decided** (2026-10-14, question 5): the member area comes first, kept narrow; the data-stream token follows, MCP after it. **Decided** (question 6): the first installation is on the intranet. The rest of the order is **proposed**. Two views were on record:

- The maintainer expects the member area to be needed early, because the researchers are not all programmers. A token and a WebSocket URL are of little use to someone without a client.
- The earlier proposal was to start with tokens on the existing listeners, as the smallest step from what runs today. Since the rules were decided this no longer holds: the existing listeners are the Observatory's filtered analyses (rule 3), and extra listeners for research would be extra connections (rule 2).

A **proposed** order that respects the first view:

| Step | Content | Depends on |
|---|---|---|
| 0 | The control API, written as a spec in this repository: [control-api.md](control-api.md), a draft | — |
| 1a | Engine, piece A: one process, many streams, added and removed at run time through the control API | 0 |
| 1b | Engine, piece B: several analyses on one decode | 1a |
| 2 | Catalogue and accounts: login by invitation and email link, own streams, private analysis pages, resource accounting and caps, audit trail | 1a; privacy notice and legal notice before it leaves the intranet |
| 3 | Filters per analysis, set in a form, with trial mode; the page shows which rules are applied; sharing a stream with invited members; the filter library | 2; a second analysis on one stream needs 1b |
| 4 | Token for the data stream of one's own analyses | 2 |
| 4b | MCP with the same tokens | 4 |
| 5 | Defined motifs per analysis; captures and trial runs | the listener's defined-motif work |
| 6 | Public research pages | 2; publishing is decided (at once, no review) |
| 7 | Audio in the browser; the language-model interpretation (in the backlog, decided 2026-10-16) | 2; questions 10 and 19 |
| 8a | Groups in the control API (1.1) and the Workbench's group pages, against the stand-in | 0; [groups.md](groups.md) |
| 8b | Engine: the group service with `activity` on wall time; then the premise test | 1a, 1b |
| 8c | Engine: its own HTTP connection, station time, impulse times; then `clock` and `coincidence` | 8b, a positive premise test |

Step 2 can be built against a stand-in for the engine as soon as step 0 is stable, and step 8a likewise. Steps 1a and 1b are what makes the Workbench real: until they exist, the intranet installation runs on the stand-in's made-up data. The engine opening the stream connection itself (see above) has to be done before sign-up is open to people the maintainer does not know. Sign-up is by invitation (decided).


## Questions and answers

Answers are the maintainer's of 2026-10-14 and are **decided**. Where an answer leaves a detail to settle, the detail is marked. Recommendations are the reviewer's and are **proposed**.

### Shaping the architecture

1. **Whose streams?** Members' own streams only at first, or the Observatory's streams too?
   - **Decided:** members' streams. Members set up and configure new streams themselves. The Observatory's streams and production setup stay outside the Workbench.
2. **On demand or continuous?** Learned motifs need continuous listening.
   - **Decided:** a stream in draft runs only while a member watches or a token reads. A stream in production may run always on. **Replaced** (decided 2026-10-15): a source connects only while an analysis using it is in a run or a preview; see [Runs](#runs). The Workbench monitors how many streams run and what bandwidth and compute they need, shows each member their own use, and enforces a cap for the server and a cap per member, both set by the master admin.
   - Detail, **proposed:** [members-and-streams.md](members-and-streams.md#states), [Resources and caps](members-and-streams.md#resources-and-caps).
3. **Who may add an analysis to someone else's stream, and what is published?**
   - **Decided:** the shared model. A stream is shared, its analyses are individual. Once connected, a stream goes offline only when all its consumers have stopped. A stream always shows who is connected to it.
   - **Decided** (question 21): what is published is an analysis.
   - **Proposed:** the owner invites members by name; the owner can still pause the stream (see [members-and-streams.md](members-and-streams.md#of-a-stream)).
4. **Who builds the engine step, and in which order?**
   - **Decided:** as recommended. The control API is written as a spec here; the engine session in the listen repository reads it against the engine's code before it counts as stable, and changes found there come back as edits to the spec here.

### Carried over

5. Member area or token access first?
   - **Decided:** the member area, kept narrow: login, registering a stream, the probe, one private analysis page and the filter form. The data-stream token follows, MCP afterwards.
6. Same server and domain as the Observatory?
   - **Decided:** a name of its own. The Workbench starts on the intranet as `low.home.arpa`. Production will be `low.vrontier.org`, which is still to be set up.
   - **Proposed** points for the intranet start: `home.arpa` names get no certificate from a public authority, so the intranet needs a local certificate authority or plain HTTP inside the network. The email login needs mail to leave the intranet. The privacy notice and legal notice are needed before the Workbench is reachable from outside.
7. Data stream: SSE, WebSocket, or both?
   - **Decided:** SSE first.
8. MCP: a process reached over SSH, or an HTTP endpoint with a token?
   - **Decided:** the HTTP endpoint, in the Workbench process.
9. May a researcher see or reuse another researcher's filters and motifs?
   - **Decided:** yes, through a shared library for all members.
   - **Proposed:** the library is part of the Workbench, not a git repository, and taking from it makes a copy. See [members-and-streams.md](members-and-streams.md#sharing).
10. Does audio leave the Workbench at all?
    - **Decided:** audio is allowed (members' question 3).
    - Asked back: is audio needed for the analysis? No. The analysis runs on the decoded samples inside the engine; nothing about it needs audio to leave the server. Audio leaving serves the researcher: hearing what a detection was is often the quickest way to tell a bird from a bearing.
    - **Proposed:** playback in the browser, per stream, switched on by its owner and off by default. It is relayed by the Workbench from the engine's one connection, never by sending the browser to the operator's server, so rule 2 holds. The engine has such a relay today (re-encoding to AAC or Opus for browsers, verified in its code). Short captures around an event count against the disk quota. No audio through the data-stream token or MCP at first. Every listener adds an encoder share and outgoing bandwidth, both accounted. For a stream the member does not run, the operator's permission has to cover playback.
11. How many analyses may run on one stream, and what does one cost?
    - **Decided:** the cost of each analysis is measured and accounted against CPU and RAM. What can and cannot be measured exactly is under [Measurement limits](members-and-streams.md#measurement-limits).
12. Who reviews what becomes public?
    - **Decided:** nobody. There is no hierarchy; the member decides.
13. What does a private analysis page show?
    - **Decided:** the spectrogram, the list of events, the motifs and an export.
    - **Proposed:** the export gives the events and the motifs of a chosen period as CSV and as JSON, with the analysis's settings and rules revision in the file. **Replaced** (2026-10-15): the export is each run's data set; see [Runs](#runs).
14. How is a token passed to the data stream?
    - Asked back: how do private APIs solve this? Almost all take the token in a request header: `Authorization: Bearer <token>` (GitHub, Stripe, OpenAI), or a header of their own (Anthropic uses `x-api-key`). Many give tokens a recognisable prefix (`ghp_` at GitHub, `sk_live_` at Stripe) so that leaked tokens can be found by secret scanning. A token in the address is avoided because addresses end up in server logs, proxies and browser histories. Browsers cannot set headers on an `EventSource`, which is why a browser page uses its session cookie. The MCP specification's authorisation for HTTP endpoints builds on OAuth 2.1, which also ends in a bearer token in the `Authorization` header.
    - **Proposed:** the `Authorization: Bearer` header with a token prefixed `low_` for programs and MCP clients; the session cookie for browser pages; never the address. OAuth only if an assistant the members use cannot be given a static header (to be checked when MCP is built).
15. What is a "project"?
    - **Decided:** there are no projects for now. A member defines streams and filters and may let others work on them.
16. Saving a rule change starts a fresh motif memory. A trial mode, or a warning?
    - **Decided:** a trial mode. And an audit trail of the creation, change and deletion of streams, their settings and their filters.
    - **Proposed:** a trial runs the changed settings as a temporary extra analysis on the same decode, next to the analysis it is a trial for. Its memory is kept in RAM only: it learns motifs as the real analysis would, so the member sees what the change does to them, and nothing is written to disk. It ends when its page is closed, or after one hour. "Keep" saves the settings as a new revision of the analysis, which then starts its new memory; "discard" ends it. A trial counts against the caps. Audit detail in [members-and-streams.md](members-and-streams.md#audit-trail).
    - **Built** against the stand-in (2026-10-15), **proposed**, adapted to fixed runs: a trial exists only next to a running run. The member changes the sources' settings on the analysis page (which during a run cannot be saved) and tries them: one engine analysis per source, `t-NNNN`, with those settings and the memory mode `ram`. Each source's page part then shows a second live panel "Trial on …" with its spectrogram, events and motifs, next to the run's. The run and its data set are not touched. "Keep" stops the run (its data set is frozen as it is) and saves the trial's settings as the next revision, ready for the next run; "discard" ends the trial; it also ends by itself after an hour or with the run. It runs whether or not a page is open. Start, discard and keep are in the audit trail.
17. How are the two repositories kept in step?
    - **Decided:** as proposed under [Staying in step](#staying-in-step).

### Raised by the answers

Answered by the maintainer on 2026-10-14; **decided**.

18. **Whose cap does the decode of a shared stream count against?** Costs are counted against CPU and RAM caps, not billed.
    - **Decided:** it is split equally among the members who have an analysis running on the stream. See [Resources and caps](members-and-streams.md#resources-and-caps).
19. **The language-model interpretation: whose key?**
    - **Decided:** each member registers their own API key for the OpenAI or the Anthropic API; the provider bills the member. Without a key there is no interpretation. Handling of the keys is **proposed** in [members-and-streams.md](members-and-streams.md#language-model-keys).
20. **Go or PHP for the Workbench?**
    - **Decided:** Go, with the HTML pages rendered by the same binary. See [Processes](#processes).
21. **What is published, a stream or an analysis?**
    - **Decided:** an analysis. See [members-and-streams.md](members-and-streams.md#of-an-analysis).

### Raised by groups

The detail is in [groups.md](groups.md#open).

22. **The name:** "group", "comparison" or "study"?
    - **Decided** (2026-10-14): none of them. It is an analysis with several sources; see [Analyses and sources](#analyses-and-sources). "Group" remains the engine's term.
23. **The first goal:** side-by-side activity first (possible on wall time), or straight to the coincidence analysis of VLF stations (needs station time)?
    - **Decided** (2026-10-15): side-by-side activity on wall time first. Before `clock` and `coincidence` are built, a small test in the engine session reads the station time from one real stream, to find out whether a relay passes the stations' timestamps on and how accurate they are.
24. **Where the group service runs:** inside the engine process or as a separate process fed by it.
    - **Decided** (2026-10-15): inside the engine process, as a module of its own. It already holds every analysis's events, so no transport and no second process are needed. The control API does not depend on it, so it can still be moved out if group analyses ever need a machine of their own.

Further questions that concern accounts, streams and limits are in [members-and-streams.md](members-and-streams.md#questions-and-answers).
