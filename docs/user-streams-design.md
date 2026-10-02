# Design: user accounts and self-registered streams

Status: draft for review (2026-10-02). Nothing here is built yet.

## Goal

People who run a live environmental stream (a hydrophone, a VLF receiver, a garden microphone)
can create an account, register their stream, and see it analysed and visualised. They need no
email to us and no root step on the server. The observatory keeps control over what appears
publicly.

Not in scope: uploading recordings, user-made visual styles, comments or social features.

## Why the current setup can't do this

Each source is a `listen-listener@<slug>` systemd unit, plus an env file and generated NGINX
routes, all installed by root. A new stream therefore needs the admin, and every restage rewrites
the full source list. Self-registration needs one long-running process that can add and remove
sources by itself.

## Architecture

```text
browser ──► NGINX ──► PHP site (pages, accounts, registration)     ──► catalogue (SQLite)
                 └──► listen-hub (all listeners, one process)      ◄── reads catalogue
```

1. **listen-hub** replaces the per-source units. It is one Go service, running as `listen`, built
   from the existing listener packages. It reads the catalogue, runs one analysis (and one ffmpeg)
   per active source, and serves every source on a single local port: `/<slug>/ws/live`,
   `/<slug>/ws/audio`, `/<slug>/api/…`. Adding, pausing or removing a source is a catalogue change
   that the hub picks up within a few seconds. Today's curated sources move into the catalogue
   unchanged, so their URLs and memories stay.
2. **NGINX** gets one generic rule instead of three per source. It forwards exactly the paths the
   browser uses (`ws/live`, `ws/audio`, `api/state/current`, `api/motifs`,
   `api/history/events?type=narrative.update`) to the hub. The rate limits stay as they are.
3. **Catalogue**: a SQLite file in `/var/lib/listen-catalogue/` with tables for users, sources,
   source_reviews and audit_log. The PHP site writes it (as `listen-web`) and the hub reads it.
   `sources.json` stays as the seed and backup for the curated sources.
4. **Accounts** in the PHP site: login by email link through the existing SMTP account, so there
   are no passwords to store. A session cookie is set after the link is used. Pages: `/login`,
   `/account`, `/account/streams/new`, and an edit page per stream.

## Registering a stream

1. **Form.** The owner gives the stream URL, name, place (optionally coordinates), time zone,
   description, credit line and link. They also tick two checkboxes:
   - "I run this stream or have the right to have it analysed".
   - "Audio may be played in the browser". This is off by default.
2. **Probe.** The hub checks the stream before it is accepted:
   - it answers, delivers audio and decodes;
   - its sample rate and level are recorded;
   - it is not silent.

   The owner sees the result immediately.
3. **Review.** The source starts as *pending*. It runs and is visible to its owner at its URL, but
   is not listed and is marked `noindex`. An admin approves it (listed), keeps it unlisted, or
   rejects it, with a note to the owner by email.
4. **Afterwards.** The owner can edit the texts, pause or delete the stream. The hub pauses a
   source that has been offline for 24 h and tells the owner. Analysis settings (frequency range,
   mains filter, transient sensitivity) start with presets: environmental, VLF radio, hydrophone.

## Filtering and limits

| Rule | Proposal |
|---|---|
| URL | `http`/`https` only. ffmpeg runs with `-protocol_whitelist http,https,tcp,tls`, so no `file:` and no other protocols. |
| Server-side request forgery | The URL's address must not be loopback, private, link-local or this server's own address; the check is repeated on every redirect and reconnect. Without this, a registered "stream" could reach internal services such as the LLM endpoint or the local llama-server. |
| Content | An `audio/*` content type, decodable by ffmpeg, at most 512 kbit/s. |
| Per user | 3 streams (raisable per account, e.g. for HamSCI). |
| Overall | 40 running sources at first. Each costs about 5 % of a core and 25–30 MB; the cap keeps the web stack and llama-server safe. |
| Texts | Length limits; links only in the credit field; reviewed before listing. |
| Abuse | A "report this stream" link on every page goes to the contact address, and an admin can suspend a source or account. |

## Rights and privacy

- The owner's attestation is stored with a timestamp. It is shown in the `rights` field and is
  where a takedown request starts.
- Accounts store only an email address and a display name. Before launch, the site needs a
  privacy notice (and an Impressum, as a German operator) covering accounts, the email login and
  logs.

## Network feeds (for providers such as HamSCI)

An organisation account can register a feed: a JSON list of its streams in the registration
format. The hub polls it, adds new streams as pending (or listed directly, for trusted
organisations) and pauses streams that drop out of the feed. That fits HamSCI's plan of 25 or more
VLF receivers better than registering each one by hand.

## Phases

| Phase | Content | Visible change |
|---|---|---|
| 0 | listen-hub and the catalogue; curated sources migrate. One last root step installs the hub and the generic NGINX rule. | None; new sources no longer need root. |
| 1 | Accounts, registration, probe, review, limits, privacy notice | Public sign-up |
| 2 | Organisation feeds | HamSCI and others feed their networks |
| 3 | Analysis presets and settings in the owner's page | Owners tune their stream |

Phase 0 is worth doing on its own: it ends the root step for every new source.

## One-time server setup (admin)

The `listen-hub` service and user (replacing the `listen-listener@` units), the catalogue directory
(`listen` and `listen-web` in a shared group), the generic NGINX rule, and `open_basedir` for the
catalogue in the PHP pool. After that, deployments are site-only or a hub binary update.

## Open questions

1. Who reviews new streams: only Mike, or a small group?
2. Is the audio relay for user streams allowed at all, or analysis only at first?
3. Limits: are 3 per user and 40 overall right to start with?
4. Should the narrator (LLM) run for user streams, or only for curated ones?
5. Is the privacy notice and Impressum covered by vrontier.org's existing pages, or does the
   subdomain need its own?
