# Audio sources

Provenance, formats and usage basis for every source in `site/_config/sources.json`.
Audio from these sources is **not** part of this repository and remains with its
respective owners. Local captures live in `samples/` (gitignored).

## Sonic Division — Satellite Radio (primary)

Found on <https://metabolicstudio.org/Sonic-Division>, post *Satellite Radio*
(`/740`, dated 5/10/24). The page is an SPA; the post body comes from
`GET /api/posts/post?tags=Sonic-Division` and embeds a plain HTML `<audio>` element.

> Metabolic Studio's Satellite Radio transmits a live feed of sounds produced by
> the Silos and the environment around Owens Dry Lake. On occasion you will hear
> in-situ performances from within the Silos. Satellite Radio is also broadcast
> across Payahuunadu via FM radio waves.

| Property | Value |
|---|---|
| URL | `https://metabolicstudio.streamguys1.com/live` |
| Transport | HTTP progressive stream (Icecast, not HLS/DASH) |
| Server | `Icecast 2.4.0-kh10` (StreamGuys CDN) |
| Content-Type | `audio/mpeg` |
| Codec | MP3, 44.1 kHz, stereo |
| Bitrate | `icy-br: 256` kbps (nominal, per server header) |
| ICY metadata | `icy-metaint: 16000`; `icy-name: Studio 1's Broadcast`; `StreamTitle` empty |
| CORS | `Access-Control-Allow-Origin: *` (a browser can fetch it directly) |
| Public listing | `icy-pub: 0` (not listed in public directories) |

## Sonic Division — Fault Line Radio loop (secondary)

Same page, post *Fault Line Radio* (`/765`). Currently a continuous loop of the
*Fire Stories* talk series, so mostly speech. It's useful as a contrast source, not
as the environmental feed.

| Property | Value |
|---|---|
| URL | `https://metabolicstudio.streamguys1.com/playback2` |
| Format | Same Icecast/MP3 setup as above: 44.1 kHz stereo, `icy-br: 256`, `icy-name: N/A` |

## Usage and credit

The page has no licence, terms or credit notes for the streams, and the API payload has none
either. The streams are publicly embedded, but that does not grant redistribution rights.

- 2026-09-30: permission requested from Sonic Division. Until 2026-10-01 the source was
  unlisted and played a 10-minute capture.
- 2026-10-01: the live stream was published as the first source on listen.vrontier.org, and
  permission requests went to all stream providers. The status per source is the `rights`
  field in `site/_config/sources.json`.
- Credit on the page: *Metabolic Studio — Sonic Division, Satellite Radio from the Silos at
  Owens Dry Lake (Payahuunadu)*, with a link to the post.
- Captures are for local development and analysis only. Never commit or publish them.

## Local captures

Recorded with `ffmpeg -c copy` (no re-encode), so the files are the MP3 bytes
exactly as the server sent them.

```sh
ffmpeg -reconnect 1 -reconnect_streamed 1 -reconnect_delay_max 10 \
  -i https://metabolicstudio.streamguys1.com/live -t 600 -c copy \
  samples/sonic-division-live-<UTC>.mp3
```

| File | Start (UTC) | Duration | Format | Size | SHA-256 |
|---|---|---:|---|---:|---|
| `sonic-division-live-20260930T083504Z.mp3` | 2026-09-30 08:35:04 (01:35 PDT) | 600.0 s | MP3 44.1 kHz stereo, VBR ≈ 93 kbps avg | 7.0 MB | `d7777f8d…b62176d6dcc` |

Observations on this capture, useful for tuning the DSP defaults:

- The level is low: RMS −39 dBFS, peak −15 dBFS, noise floor ≈ −41 dBFS. Use adaptive or
  normalised thresholds, not fixed ones.
- The actual bitrate is VBR averaging about 93 kbps, well below the declared `icy-br: 256`. The
  encoder low-passes at about 11–12 kHz, so `max_hz` ≈ 11 kHz is the useful ceiling.
- Content: continuous low-frequency (wind-like) energy below ~150 Hz, persistent tonal bands
  near ~700 Hz and ~1.5 kHz (candidate resonances), and a few broadband transients (at about
  95 s and 375 s).

## Fallback sources (for testing)

- dublab: the *Metabolic Sonics* archive (Metabolic Studio's show on dublab). These are
  recorded episodes, useful as fixed test files.
- NASA: public-domain space and mission audio, for example the NASA SoundCloud
  and the Juno/Voyager sonifications. Use it where the licence has to be clean.
- Metabolic Studio Sonic Division SoundCloud: the *PPG Sound Library* set (Silo
  recordings) and the *Sound Map* set. Same owners, so the same permission caveat applies.

## VLF Natural Radio, Heidelberg (live, source `vlf-heidelberg`)

| Property | Value |
|---|---|
| Page | https://dk7fc.info/vlfstreams.html (operator: Stefan Schäfer, DK7FC) |
| URL | `https://dk7fc.info/live-stream.php?stream=vlf15` (VLF receiver near Heidelberg, Germany) |
| Format | Ogg Vorbis, 32 kHz (VLF up to 16 kHz); analysed at `-sample-rate 32000 -transient-k 10 -transient-gap 5s` |

The stream page states no licence or terms. Credit on the page: *VLF receiver Heidelberg, Stefan
Schäfer*, with a link to the stream page. Permission was requested on 2026-10-01.

## Mars · InSight (archive, source `mars-insight`)

Loop `samples/mars-insight-seis.flac` (1268.7 s; on taurus in /var/lib/listen/samples/), built on
2026-09-30 from NASA "Sounds from Beyond" (https://www.nasa.gov/sounds-from-beyond/). Each clip was
loudness-normalised (loudnorm I=-20) at 48 kHz stereo, with 3 s of silence between clips, in this order:

| File (www.nasa.gov/wp-content/uploads/2015/01/…) | Content | Length |
|---|---|---:|
| Quake-Sol-173.wav | Magnitude 3.7 marsquake, 2019-05-22, sonified | 46.9 s |
| Cropped-Dinks-and-Donks-sample.wav | SEIS "dinks and donks": components cooling at night | 120.1 s |
| 20190819-Sol-98-SEIS-Spatialized-reflective.wav | Robotic arm camera scan with wind, SEIS | 111.0 s |
| 08-Brian-Cook_raw_velocity_0.6_normalisedx1_2octavesUp_03.wav | Wind vibrations, raised two octaves | 20.0 s |
| Quake-Sol-235.wav | Magnitude 3.3 marsquake, 2019-07-25, sonified | 15.7 s |
| 06-MASTERRESAMPLED-48Kraw_velocity_0.6_normalisedx1.wav | Raw seismometer wind vibrations | 937.0 s |

Left out: 07-Mars_sound1a_20s_x100.wav (raw data at 100 Hz, meant to be sped up ×100).
Credit: NASA/JPL-Caltech/CNES/IPGP. Use: NASA media usage guidelines (free to use, credit NASA,
no implied endorsement).

## Jupiter · Juno (archive, source `jupiter-juno`)

Loop `samples/jupiter-juno-waves.flac` (220.5 s), built the same way:

| File | Source | Content | Length |
|---|---|---|---:|
| e2-wave-ganymede-flyby-compressed.wav | NASA Sounds from Beyond (…/uploads/2024/05/) | Juno Waves: Ganymede flyby, 2021-06-07 | 49.0 s |
| jno-bkom-16-240.mp3 | U Iowa, …/plasma-wave/juno/audio/201608/ | Broadband kilometric radiation (bKOM), 2016 | 24.8 s |
| jno-PJ4-Elo-17-033-1248-1250-slow.mp3 | U Iowa, …/juno/audio/201702/ | Perijove 4, near the equator: plasma waves, 2017 | 122.9 s |
| jno-E45-LFRH-22-272-0836-1006-1st-try-Matlab-modified.mp3 | U Iowa, …/juno/audio/202209/ | Europa flyby, 2022 | 11.8 s |

Credit: NASA/JPL-Caltech/SwRI/University of Iowa. The University of Iowa clips are licensed
Creative Commons Attribution 3.0 Unported (stated on each clip page); NASA clip under NASA media
guidelines. Voyager plasma-wave audio (also Iowa) is "All rights reserved": not used, ask first.

Analysis settings for both: default sample rate, `-loop -transient-k 12 -transient-gap 6s`.

## Amsterdam · Hydrophile 1 and 5 (live, sources `amsterdam-hydrophile-1`, `amsterdam-hydrophile-5`)

Added on production by the IONOS admin at the user's request on 2026-10-01, then synced into the local sources.json.

| Property | Value |
|---|---|
| URLs | `https://locus.creacast.com:9443/amsterdam_hydrophile_1.mp3`, `…/amsterdam_hydrophile_5.mp3` |
| Format | MP3, 44.1 kHz stereo, ~64–74 kbps; icy-description "Live audio stream by lia mazzari" |
| Locations | 1: 52° 22′ 3.4″ N, 4° 54′ 14.9″ E · 5: 52° 20′ 51.7″ N, 4° 54′ 45.8″ E |
| Project | Soundcamp / Acoustic Commons streambox (https://soundtent.org/streambox/), on the Locus Sonus Icecast server (locus.creacast.com); public list: https://locus.creacast.com:9001/, map: https://locusonus.org/soundmap/. Corrected 2026-10-01: not radio.earth. |

Credit: Lia Mazzari — Soundcamp streambox (stream metadata: "Live audio stream by lia mazzari"). No licence is stated for the streams. They are publicly listed on radio.earth for listening, but redistributing the audio (our `-audio` relay) is not explicitly allowed. Ask Lia Mazzari / Soundcamp (contact at soundtent.org) if this becomes more than an experiment.

## radio.earth: Bayelva, Brno, Kozmice (live; added 2026-10-01)

| Slug | Port | Stream | Format (probed 2026-10-01) | Page |
|---|---|---|---|---|
| `svalbard-bayelva` | 8101 | `https://radio.aporee.org:8443/bayelva` | MP3 44.1 kHz stereo, ~160 kbps; mean −36 dB | https://radio.earth/bayelva/ |
| `brno-luzanky` | 8102 | `https://locus.creacast.com:9443/brno_luzanky.mp3` | MP3 44.1 kHz stereo, ~200 kbps; mean −15 dB, peaks at 0 dBFS | https://radio.earth/brno/ |
| `kozmice-meadows` | 8103 | `https://locus.creacast.com:9443/kozmic.mp3` (alt: radio.aporee.org:8443/kozmic.mp3) | MP3 44.1 kHz stereo, ~140 kbps; mean −40 dB | https://radio.earth/kozmic/ |

The stream URLs come from each page's player script.
- Bayelva: Common Grounds (Julia Boike/AWI, Kerstin Ergenzinger, Bnaya Halperin-Kaddari; Sono-Choreographic Collective; Bauhaus-University Weimar).
- Brno: Tomáš Šenkyrík, Vit Kalvoda, Udo Noll (since spring 2021).
- Kozmice: Kozmické ptačí louky, curated by Magdaléna Manderlová and Michal Kindernay; the station is by Udo Noll (2025). Coordinates are taken from the page's aporee map link.

Same licence caveat as for the Amsterdam hydrophones: there is no stated licence, so relaying the audio needs their permission if this becomes more than an experiment.
