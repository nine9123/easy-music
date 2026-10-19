# easy-music

A small self-hosted music server for one person's library. A single Go binary
(standard library only), a web UI with no build step, and a Docker image.

- **Library**: scans read-only directories (MP3, FLAC, Ogg/Opus, M4A/AAC, WAV),
  reads tags, falls back to `Artist/Album/01 - Title` paths. Browse tracks,
  albums and artists, search, sort by plays, last played, date added and more.
- **Music videos**: put `Song.mp4` (or `.webm`/`.mkv`/`.mov`) next to
  `Song.flac` and the track gets a video. One click switches between listening
  and watching without losing your place. A video file on its own is a track
  too.
- **Import from URL**: paste a YouTube link (or anything
  [yt-dlp](https://github.com/yt-dlp/yt-dlp) supports) and get a local copy:
  video plus a separate audio file, cover and metadata, optionally straight into
  a playlist.
- **Radio mode**: "more like this" from any track, with variety enforced (see
  [below](#radio-mode)). Shuffle uses the same rules.
- **Playlists** (drag tracks, albums, artists, imports or queue entries onto
  one in the sidebar; drag rows to reorder), **favorites**, **listening
  stats** (plays, skips, time listened, top tracks/artists, history).
- **Internet radio**: add a stream URL, a link to a `.pls`/`.m3u`, or upload
  one.
- **Covers**: `cover.jpg`/`folder.jpg`, embedded art, import thumbnails, and
  optionally MusicBrainz / Cover Art Archive for the rest. Cached downscaled on
  the SSD.
- **HDD-friendly**: the index, covers and copies of your most-played tracks
  live on the SSD cache, so browsing never touches the HDD and everyday
  listening rarely does.
- **Built for [idlegate](https://github.com/nine9123/idlegate)**: sleeps when
  unused, wakes on the next request, and loses nothing in between (see
  [below](#running-behind-idlegate)).

## Run it

```sh
docker compose -f docker-compose.example.yml up -d
```

[`docker-compose.example.yml`](docker-compose.example.yml) runs easy-music
behind idlegate with the four mounts described next. Open port 8080.

Without Docker: `go build && MUSIC_DIRS=~/Music DOWNLOAD_DIR=~/Music/imports
CACHE_DIR=./data/cache STATE_DIR=./data/state ./easy-music` (imports need
`yt-dlp` and `ffmpeg` on `PATH`).

## Storage

| Mount | Disk | Written? | Contents | If lost |
|---|---|---|---|---|
| `/music` | HDD | never (`:ro`) | your library | – |
| `/downloads` | HDD | yes | URL imports (`youtube/<id>.mp4/.m4a/.info.json/.jpg`), scanned like `/music` | re-import |
| `/cache` | SSD | yes | `library.json` index, `covers/`, `hot/` (copies of most-played tracks) | rebuilt automatically |
| `/config` | SSD | yes, small | `state.json`: play counts, favorites, playlists, stations, import queue | **gone — back this up** |

Everything in `/cache` can be deleted at any time. Track IDs are derived from
file paths relative to their library directory, so a rebuilt index has the same
IDs and playlists, stats and favorites still line up (moving or renaming a file
gives it a new ID; playlists keep the old entry, marked missing).

**Hot cache.** Once a track has been played `HOTCACHE_MIN_PLAYS` times it is
copied to `/cache/hot` and streamed from there. When `HOTCACHE_SIZE` is full,
the least-listened copies (plays, decaying over a month without listening) make
room, but never for a track that is colder than they are. Copies are checked
against the size and mtime in the index, so serving one doesn't touch the HDD;
a changed or deleted source is pruned on the next scan.

## Configure

| Variable | Default | Meaning |
|---|---|---|
| `LISTEN` | `:8080` | listen address |
| `MUSIC_DIRS` | `/music` | library directories, comma-separated |
| `DOWNLOAD_DIR` | `/downloads` | writable directory for imports; empty disables them |
| `CACHE_DIR` | `/cache` | rebuildable data (SSD) |
| `STATE_DIR` | `/config` | non-rebuildable data |
| `SCAN_ON_START` | `true` | rescan the library at every start |
| `SCAN_INTERVAL` | `0` (off) | periodic rescan, e.g. `6h` |
| `HOTCACHE_SIZE` | `4G` | SSD space for most-played tracks; `0` disables |
| `HOTCACHE_MIN_PLAYS` | `3` | plays before a track is copied to the SSD |
| `COVER_FETCH` | `true` | look up missing album covers on MusicBrainz / Cover Art Archive |
| `YTDLP` | `yt-dlp` | yt-dlp binary |
| `YTDLP_ARGS` | – | extra yt-dlp arguments, e.g. `--cookies /config/cookies.txt` |

There is no login. Run it on your LAN, behind a VPN, or behind a reverse proxy
that does authentication.

## Radio mode

Press the radio button (or "Start radio" on any track) and the queue keeps
filling with music similar to that track. The mixer
([`internal/mix`](internal/mix/mix.go)) scores every track against the seed
*and* the previous pick, from:

- **genre**: whole genres and genre words (`Progressive Rock` ~ `Rock`);
- **era**: release years close together;
- **your listening**: artists you play in the same session, or keep next to
  each other in playlists, count as related. This is the strongest signal, so
  the station learns your taste beyond what the tags say;
- small nudges: favorites up, often-skipped and just-played tracks down.

Then the variety rules apply, as hard rules rather than weights:

- an artist doesn't come back for **8 tracks**, and featured artists count
  (`A feat. B` blocks both A and B);
- an album doesn't come back for **30 tracks**;
- the candidate pool holds at most two tracks per artist, and the pick is a
  weighted random draw from it, so the same seed gives a different station each
  time;
- each pick blends the seed with the previous track, so the station drifts
  rather than circling one sound.

The rules only relax when nothing passes them (a library of three artists).
Shuffle is the same machinery without a seed: random picks, same gaps, gently
nudged to sound a bit like the previous track.

Skipping a track in the first 30 seconds counts as a skip; listening to half of
it (or 4 minutes) counts as a play.

## Music videos and imports

A track has a video when a video file with the same base name sits next to the
audio. The video button (or `v`) plays it in place of the audio, from the same
position; hide it and you're back to audio only. Videos are streamed with Range
support, so seeking works.

An import (Import page, or the box at the top of a playlist) runs yt-dlp
with:

- best video up to 1080p, preferring H.264 + AAC so every browser plays it,
  merged into `<id>.mp4`;
- the audio extracted to `<id>.m4a`, so listening doesn't stream the video;
- `<id>.info.json` (title, artist, URL) and `<id>.jpg` (cover) next to them.

The library reads those sidecars, so an import is an ordinary track with a
music video, survives an index rebuild, and gets its artist/title from the
video's metadata ("Artist - Title (Official Video)" is cleaned up). Imports run
one at a time from a queue that is saved in `/config`. A failed import can be
retried. yt-dlp needs regular updates to keep working with YouTube: rebuild the
image to get the latest release (on Gitea, run the "Build and deploy" workflow
by hand; it also runs on every push to `main`).

## Running behind idlegate

[idlegate](https://github.com/nine9123/idlegate) stops containers that have
been idle and starts them on the next request. easy-music is built for that:

- **Fast wake.** It listens as soon as the cached index is loaded, so the first
  page is served right away. `/healthz` is there for idlegate's `health=`
  option.
- **Catch-up on start.** The rescan (`SCAN_ON_START`), queued imports and
  hot-cache refills run at startup, because they can't run while the container
  is stopped. Set `SCAN_ON_START=false` if you'd rather the HDD only spin up
  when you play something.
- **No stop loses anything.** State is written atomically about two seconds
  after every change. The library scan checkpoints every 30 seconds, so a huge
  first scan interrupted by a stop resumes where it was. An import interrupted
  by a stop goes back to the queue, and yt-dlp resumes its partial download on
  the next start. Shutdown finishes in about 3 seconds, well inside idlegate's
  10-second stop grace.
- **Awake while you listen, asleep when you don't.** Streams and videos keep
  the container awake for as long as they transfer, and radio stations are
  relayed through the server for the same reason. Browsers buffer whole
  songs, though, so while something is playing the UI also sends a small
  heartbeat every 4 minutes. Paused or closed: no heartbeat, and the server
  goes to sleep after idlegate's `idle=` time.
- **No idle polling.** The UI polls only while a scan or an import is running.
  A tab left open on a paused player doesn't keep the server awake.

An import keeps going only while the container is awake. Leave the Import page
open while it downloads (its progress polling keeps the container awake), or
let it resume on the next visit.

## API

JSON over HTTP; the web UI uses nothing else.

| | |
|---|---|
| `GET /api/tracks?q=&filter=favorites\|video\|imported\|unplayed&artist=&album=&sort=&order=&offset=&limit=` | query tracks (`sort`: `title`, `artist`, `album`, `year`, `duration`, `added`, `plays`, `lastPlayed`, `favorited`, `random`) |
| `GET /api/tracks/{id}` · `/stream` · `/video` · `/cover` | metadata, audio, video, cover |
| `POST /api/tracks/{id}/played` `{"seconds"}` · `/skipped` | listening stats |
| `PUT`/`DELETE /api/tracks/{id}/favorite` | favorites |
| `GET /api/albums` · `/api/artists` · `/api/stats` | browsing and stats |
| `POST /api/mix` `{"seed", "recent": [...], "count"}` | radio mode / shuffle picks |
| `GET`/`POST /api/playlists`, `GET`/`PATCH`/`DELETE /api/playlists/{id}`, `POST /api/playlists/{id}/tracks` | playlists |
| `GET`/`POST /api/stations` (JSON or `.pls`/`.m3u` upload), `PATCH`/`DELETE /api/stations/{id}`, `GET /api/stations/{id}/stream` | internet radio |
| `GET`/`POST /api/downloads` `{"url", "playlistId"}`, `POST /api/downloads/{id}/retry`, `DELETE /api/downloads/{id}` | imports |
| `GET /api/status` · `POST /api/scan` · `POST /api/now-playing` · `GET /healthz` | housekeeping |

## Develop

```sh
go vet ./... && go test -race ./...
docker build -t easy-music .
```

The tag-reader tests run against small files produced by ffmpeg
(`internal/tags/testdata`), the import tests against a fake yt-dlp script
(`internal/youtube/testdata/fake-yt-dlp`), and the API tests run the whole
server against a temporary library.
