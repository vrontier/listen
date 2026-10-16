# Listening Observatory Workbench

**Listening Observatory Workbench (LOW)** is the research companion to [Listening Observatory](https://github.com/vrontier/listen): tools for researchers to explore live audio streams with their own filters, motifs and AI-assisted analysis.

The Observatory is a small, curated, public exhibit: a handful of streams, each analysed one way, with no accounts. The Workbench is where individual exploration happens. A researcher brings a stream, decides how it is analysed, and gets the results as pages, as a data stream, or through an AI assistant.

## Status

First part of the member area, built against a stand-in for the engine; the engine process itself does not exist yet. The specs in [`docs/`](docs/) record what has been decided, what is proposed and what is still open.

Working: invitation by the master admin, login by email link, registering a source with a probe, draft and production, pausing and deleting, analyses with one or more sources and settings per source, the analysis page grouped per source (live spectrogram, events, motifs, settings and filter rules with validation by the engine, memories), revisions, runs that fix one revision and produce a data set each (events and comparison as CSV and JSON) with a Runs page to manage them, a ten-minute preview that records nothing, a trial of changed settings next to a run, comparing two or more sources on one time axis during a run (activity per second, correlations, a map with distances and the night side; on the stand-in's made-up data), usage accounting with caps for the server and per member and a disk quota counting data sets and memories, sharing a source with other members, a Results page with the data sets of runs to download or delete and the disk space they take, tokens for programs with a data stream (JSON and Server-Sent Events) and an MCP endpoint, publishing an analysis as a public research page with reports to the master admin, members' language-model API keys (checked with the provider, stored encrypted), runs that stop by themselves when a source stops sending (with a mail when a run ends), the operator's contact for streams the member does not run, coordinates rounded to about 10 km on public pages, a limit of live visitors per published analysis, the list of who is connected, the audit trail. Not yet: the engine itself, the timing comparisons that need station time, the language-model interpretation, motif definitions and captures.

## Running it for development

Needs Go 1.26 or later. The stand-in serves the control API of [docs/control-api.md](docs/control-api.md) with made-up data and connects to no stream.

```sh
go build ./cmd/...
./listen-standin -socket engine.sock &
./workbench -db workbench.db -engine engine.sock -base-url http://127.0.0.1:8090 &
./workbench -db workbench.db -base-url http://127.0.0.1:8090 admin -email you@example.org -name "Your Name"
```

The last command creates the master admin and prints a login link. Without `-smtp`, login links and invitations are written to the Workbench's log. A Unix socket path may be at most about 100 characters long. The stand-in's probe answers by words in the address: `unreachable`, `notaudio`, `loud` and `silent` give the failures, `.ogg` a 32 kHz stream.

Tests: `go test ./...`. The engine's control API version this Workbench needs: **1.0**.

`make run` builds both programs and starts them together.

## Deploying

`make deploy intranet` runs the tests, builds both programs for Linux and installs them on the intranet host as `low.home.arpa`. That host's SSH name goes in `deploy/local.mk` (ignored by git) as `INTRANET_HOST = …`; the account needs sudo without a password. `make deploy production` is reserved for `low.vrontier.org`, which is not set up yet.

What `deploy/install.sh` sets up there:

| Part | Where |
|---|---|
| User and group `low` | system user, no login |
| Programs | `/usr/local/bin/low-workbench`, `/usr/local/bin/low-standin` |
| Engine | `low-engine.service`, socket `/run/low-engine/control.sock`; for now the stand-in, with made-up data |
| Workbench | `low-workbench.service` on 127.0.0.1:8110, catalogue `/var/lib/low/workbench.db` |
| Settings | `/etc/low/workbench.env` (base address, mail server, the sealing key of language-model keys); created once, then kept. Back up its `LOW_KEYS_SECRET` apart from the catalogue: without it, saved keys cannot be opened |
| nginx | `/etc/nginx/sites-available/low.home.arpa` and `snippets/low-workbench-proxy.conf`; only the networks given in `deploy/local.mk` (`INTRANET_ALLOW`), plus the host itself |
| TLS | key and signing request `/etc/ssl/home-arpa/low.{key,csr}`, made on the host. Until `low.crt`, signed by the internal CA, is installed there, the site is served over plain HTTP; the next deploy after that switches to HTTPS. |

`make admin intranet EMAIL=… NAME=…` creates a master admin there and prints a login link. Without a mail server in `workbench.env`, login links and invitations appear in `journalctl -u low-workbench`.

## What it will offer

- **Public presentation of research:** pages that show ongoing work to visitors.
- **A member area for researchers:** usable without programming.
- **Streams:** register streams, analyse them in runs, and share them with invited members.
- **Filters and motifs:** define what the analysis leaves out and what it looks for, try a change before keeping it, and share filters through a common library (taken out of the Workbench for the time being).
- **A data stream by token:** analysis data over Server-Sent Events.
- **An MCP interface by token:** for AI-supported research.
- **Groups:** several streams aligned on one time axis and compared, for example to tell lightning heard at several stations from local interference at one.

## How it relates to Listening Observatory

```text
                 ┌──► Listening Observatory   curated public exhibit, no accounts
listener ────────┤
(shared engine)  └──► Workbench               accounts, tokens, own streams, own analyses
```

Both projects run on the same listener, the Go service in the Observatory's repository that decodes a stream and analyses it. The Workbench adds accounts, tokens and configuration on top; it does not fork the analysis. What this means for running a Workbench is under [Dependencies](#dependencies).

Four rules shape the design (details in [docs/design.md](docs/design.md)):

1. **One shared engine.** Analysis code lives in the listener and is used by both projects.
2. **One connection per stream.** However many researchers work on a stream, its operator sees one connection. Each researcher's filters and motifs run as an additional analysis on the same decoded audio.
3. **The Observatory's filters never apply here.** A filter is a property of one analysis, not of the stream. The Workbench works on raw audio or on its own analysis, with its own rules or none.
4. **Researchers bring their own streams.** A stream is added by the person who runs it, or with the explicit permission of its operator.

## Dependencies

The Workbench does not run on its own. It needs the engine, which lives in another repository.

- **Required: the engine.** One program built from [github.com/vrontier/listen](https://github.com/vrontier/listen) (directory `listener/`), running on the same host next to the Workbench's own process. The host also needs ffmpeg, which the engine uses to decode audio.
- **Not required: a Listening Observatory.** The Workbench needs none of the Observatory's website, source list, per-stream listeners or narrator setup. It can run on a server where no Observatory exists.
- **Sharing a host with an Observatory** is needed in one case only: when the Workbench is to analyse streams that the Observatory also analyses. Both must then use one engine process, because of the one-connection-per-stream rule.
- **Two repositories.** Anyone running a Workbench fetches and builds from two repositories. Moving the engine into a repository of its own is a possible later step; it is not planned now.
- **Staying in step (proposed).** The engine reports the version of its control API, the Workbench checks it at start, and this repository states the minimum engine version it needs.
- **What exists.** The engine process the Workbench needs is not built yet. Today's listener runs one analysis of one stream per process.

Details: [docs/design.md](docs/design.md#dependencies).

## Documents

| Document | Content |
|---|---|
| [docs/design.md](docs/design.md) | Goals, the split from the Observatory, the design rules, dependencies, the processes, features, order of work, open questions |
| [docs/analyses-and-filters.md](docs/analyses-and-filters.md) | One decode with several analyses; filter rules; what is and is not filtered; motifs |
| [docs/members-and-streams.md](docs/members-and-streams.md) | Accounts, registering a stream, states of a stream, limits, rights and privacy |
| [docs/groups.md](docs/groups.md) | Analysing several streams together: groups, station time, the first group analyses |
| [docs/control-api.md](docs/control-api.md) | The contract between the Workbench and the engine process: streams, analyses, validation, probe, data, versions |

## Third-party parts

| Part | Use | Licence |
|---|---|---|
| [JetBrains Mono](https://www.jetbrains.com/lp/mono/) 2.304 | the pages' font, served by the Workbench (`internal/web/static/fonts`) | SIL Open Font License 1.1 |
| [Leaflet](https://leafletjs.com) 1.9.4 | the map pop-up for a stream's coordinates, served by the Workbench (`internal/web/static/leaflet`) | BSD 2-Clause |
| [OpenStreetMap](https://www.openstreetmap.org/copyright) | map tiles and place search in that pop-up, loaded by the member's browser from openstreetmap.org | data ODbL; tiles and search under the OpenStreetMap Foundation's usage policies |
| [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) | the catalogue database | BSD 3-Clause |

The map pop-up is the only place where a member's browser contacts a third party; the privacy notice has to say so.

## Licence

MIT, like Listening Observatory. See [LICENSE](LICENSE). Audio remains with its sources and is never stored in this repository.
