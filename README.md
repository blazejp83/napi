<p align="center"><img src="docs/logo.svg" alt="Napi" width="420"></p>

# Napi

Subtitles for your films in one right-click. Napi finds Polish (or English) subtitles on [NapiProjekt](https://napiprojekt.pl) and [OpenSubtitles](https://www.opensubtitles.com) for single videos or whole folder trees. When no subtitles exist for your exact release, it takes the closest one and **syncs it to your file's audio**.

Windows 10/11, two portable executables, no installer, no admin rights.

## Features

- **Explorer menu:** right-click a video, a selection, a folder or a folder background → *Pobierz napisy* (your languages, Polish by default) or *Pobierz napisy (eng)*.
- **Progress card** in the bottom-right corner: the current step and file, *Anuluj*, then the result with *Pokaż plik*, *Szukaj na napiprojekt.pl* and *Szczegóły*. It closes itself a few seconds after a clean success.
- **Job queue:** start more downloads, syncs or `napiprojekt:` links while one is running; they queue in the same card, run one after another, and queued ones can be removed with ✕.
- **Other releases, synced:** if nothing matches your exact file, Napi picks the best subtitles made for another release of the same film or episode and aligns them with [alass](https://github.com/kaegi/alass). It handles offsets, cuts and 25 vs 23.976 fps.
- **Re-sync** existing `.srt`/`.sub` files: right-click → *Synchronizuj napisy*, one file or many.
- **`napiprojekt:` links:** click a subtitle version on a film's napiprojekt.pl page and Napi downloads it and syncs it to the video that was waiting for it.
- **Safe with existing subtitles:** files are named per language (`Film.pol.srt`, `Film.eng.srt`); Napi asks before replacing and keeps the old file as `.bak`.
- **Settings window** with light/dark theme, plus a full **command-line tool** for batch jobs and scripts.

## Install

1. Download the latest `napi-<version>-windows-amd64.zip` from [Releases](https://github.com/blazejp83/napi/releases) and unzip `napi.exe` and `napiw.exe` into a permanent folder, e.g. `%LOCALAPPDATA%\Programs\napi`.
2. Double-click `napiw.exe` and switch on *Menu Eksploratora i linki napiprojekt:*, or run `napi menu install`. This registers for the current user only. On Windows 11 the entries are under *Show more options* (Shift+F10).
3. Optional: add your OpenSubtitles account (below) for the fallback and English subtitles.

Remove with the same switch or `napi menu uninstall`. Settings live in `%APPDATA%\napi\napi.json`; for a portable setup put `napi.json` next to the executables (`napi config init --portable`).

`napiw.exe` needs the Microsoft Edge WebView2 runtime, which ships with Windows 10/11. `napi.exe` doesn't.

## Using it

| You do | Napi does |
|---|---|
| Right-click a video or folder → *Pobierz napisy* | Downloads subtitles for every video (subfolders included, `Featurettes`/`Extras`/`Trailers`… skipped) and shows progress in the corner card |
| …the film already has subtitles in that language | Asks first: *Pobierz nowe* / *Zostaw*, or for a batch *Tylko brakujące* / *Wszystkie od nowa* |
| *Pobierz napisy (eng)* | The same in English only; Polish files next to the film don't block it |
| Right-click `.srt` files → *Synchronizuj napisy* | Finds each file's video (`Film.pol.srt` → `Film.mkv`), syncs it to the audio and saves it in place (old one as `.bak`) |
| Nothing found, but NapiProjekt knows the film | The card offers *Szukaj na napiprojekt.pl*: the site's search opens in your browser with the title filled in; pick the film → *napisy* → a version, and its `napiprojekt:` link syncs it to your file |
| Start `napiw.exe` | Settings: languages, format, encoding, naming, OpenSubtitles login with a connection test, Explorer menu |

Synced subtitles are always labelled as such (and machine translations as machine translations), because a different cut can still leave a scene off. Give them a quick look.

## OpenSubtitles

NapiProjekt needs no account. OpenSubtitles adds exact-file matches for releases NapiProjekt lacks, the other-release fallback and English subtitles:

1. Create a free account at [opensubtitles.com](https://www.opensubtitles.com) and an API consumer at <https://www.opensubtitles.com/consumers>.
2. Enter the API key, username and password in the settings window and press *Sprawdź połączenie*, or put them in `napi.json`:
   ```json
   "openSubtitles": { "apiKey": "…", "username": "…", "password": "…" }
   ```
3. `napi account` shows today's remaining downloads; `napi search Film.mkv` lists the ranked candidates without downloading.

Free accounts have a daily download limit (20 at the time of writing). When it's reached, Napi stops using OpenSubtitles for the rest of the run. The password is stored in plain text in `napi.json`, and the login token is cached for 20 hours in `%LOCALAPPDATA%\napi`.

## Command line

```
napi Film.mkv                         # same as "napi get Film.mkv"
napi get D:\Filmy E:\Seriale -l PL,ENG -r raport.csv
napi get . --no-recursive --force     # this folder only, replace existing
napi sync Film.pol.srt more.srt       # re-sync subtitle files to their videos
napi sync D:\Seriale\*.pol.srt        # wildcards work in cmd too
napi sync Film.mkv inne-napisy.txt    # align other subtitles to a video
napi sync Film.mkv napiprojekt:<hash> # …or a version linked on napiprojekt.pl
napi search Film.mkv                  # OpenSubtitles candidates, best first
napi account                          # OpenSubtitles login and downloads left
napi hash Film.mkv                    # NapiProjekt hash, frame rate
napi config [init [--portable]]       # show or write the settings file
napi menu install | uninstall         # Explorer menu and napiprojekt: links
```

| flag (`get`) | default | |
|---|---|---|
| `-l, --lang` | `PL` | languages in order of preference: `PL`, `ENG`, `PL,ENG` |
| `--format` | `srt` | `srt` converts every format to SubRip; `original` keeps the server's format |
| `-e, --encoding` | `utf8bom` | `utf8bom`, `utf8`, or `cp1250` for old TVs and players |
| `-n, --naming` | `iso639-2` | `iso639-2` (`Film.pol.srt`), `iso639-1` (`Film.pl.srt`), `none` (`Film.srt`) |
| `-f, --force` | off | replace existing subtitles in that language (old ones kept as `.bak`) |
| `--no-recursive` | off | don't descend into subfolders |
| `-j, --jobs` | `4` | videos in parallel; API requests are spaced at least 200 ms apart |
| `--fps` | `23.976` | frame rate for MicroDVD subtitles when the video's can't be read |
| `-r, --report` | | write a CSV report |

Exit code: `0` all videos have subtitles, `2` some weren't found, `1` errors. The Explorer menu uses the same settings and writes its last report to `%LOCALAPPDATA%\napi\ostatnie.csv`.

## How it works

**Where subtitles come from.** For each video, in order (the exact steps once per language):

1. **NapiProjekt, exact file:** MD5 of the first 10 MiB, `POST api-napiprojekt3.php` (`mode=17`).
2. **OpenSubtitles, exact file:** the OpenSubtitles moviehash.
3. **Same film or episode, another release:**
   - **Finding the film:** NapiProjekt usually knows which film a file belongs to even without subtitles (`mode=2` returns title, year, IMDb ID, and series/season/episode). Failing that, Napi uses the IMDb ID from the hash search, or the title and year from the file name, matched against original and alternative titles, accents ignored.
   - **Ranking:** language, human over machine translation, release-name similarity (source, resolution, codec, group), frame rate, popularity.
   - **Checks and sync:** a candidate whose length doesn't fit the video is skipped. The rest is converted to SRT and synced with alass; up to three candidates are tried.
4. **Nothing found but the film is known:** napiprojekt.pl's public search opens in your browser with the film's title. A small local page submits the search form; the film's list of versions is two clicks away and needs no login.
   - **Why it's manual:** the API has no call that lists versions, and the site is behind a Cloudflare bot check that only a real browser passes.
   - **No direct link:** the film page the API's ID leads to needs a login.

**Broken timing** in downloaded subtitles is repaired: a line lasting longer than 30 s (a mistyped end time) is cut back to a normal reading time, and the length check uses start times, so one bad line can't reject a file.

**Existing subtitles** are recognised per language from the name (`.pl`, `.pol`, `.polish`, `.en`, `.eng`…) or, for untagged files like `Film.srt`, from their text.

**Formats and charsets:**
- **Read:** MicroDVD, MPL2, TMPlayer and SRT, in UTF-8, windows-1250 or ISO-8859-2.
- **Written:** SRT by default.
- **MicroDVD frame rates** come from the file's own header, then the video's MKV/MP4/AVI header, then `ffprobe`, then `--fps`.

**Sync tool:** the first time a sync is needed, Napi downloads alass 2.0.0 for Windows (26 MB, includes ffmpeg, SHA-256 pinned) into `%LOCALAPPDATA%\napi\tools`; `alassPath` in `napi.json` points to your own. A sync reads the whole audio track: about 30–60 s for a feature film, longer from a network drive.

**Multi-select and the job queue:** Explorer starts one process per selected file (a browser one per `napiprojekt:` link). Each drops its work into `%TEMP%\napi-queue` and exits, except the one holding a named mutex: it shows the card and keeps polling the spool while open. A burst of launches within 0.4 s (one selection) becomes one job; jobs run one at a time, because each sync reads a whole audio track. *Anuluj* cancels the running job only.

**Interface:**
- **Wails app:** `napiw.exe` is a [Wails](https://wails.io) v2 app with an embedded HTML/CSS/JS frontend (`internal/gui/web`, no npm build), rendered by WebView2.
- **Windows:** the corner card is a frameless acrylic tool window placed above the taskbar; the settings window uses Mica.
- **Logo:** the *Sync* mark is drawn by `tools/mkicon` into `assets/napi.ico`, with simplified drawings for 16–40 px.

## Building

Requires Go 1.24+; cross-compiles from Linux or WSL.

```
./build.sh 0.7.0    # dist/napi.exe, dist/napiw.exe and dist/napi-0.7.0-windows-amd64.zip
go test ./...       # NAPI_LIVE=1 also queries napiprojekt.pl
```

`build.sh` embeds the icon, version info and manifest with [go-winres](https://github.com/tc-hib/go-winres), and builds `napiw` with the `desktop,production` tags Wails needs.

| package | |
|---|---|
| `cmd/napi`, `cmd/napiw` | command-line tool and Windows interface |
| `internal/napi`, `internal/osdb` | NapiProjekt and OpenSubtitles clients |
| `internal/batch` | the per-video pipeline, existing-file handling, re-sync |
| `internal/subs`, `internal/media` | subtitle formats and charsets; frame rate and duration from video headers |
| `internal/subsync` | alass download and invocation |
| `internal/gui`, `internal/winui` | corner card and settings (Wails); Explorer menu, queue, Win32 helpers |

## Licence and credits

Napi is released under the [MIT licence](LICENSE).

- **Services:** subtitles come from [NapiProjekt](https://napiprojekt.pl) and [OpenSubtitles](https://www.opensubtitles.com). Napi is not affiliated with either; please respect their terms and limits.
- **Sync:** done by [alass](https://github.com/kaegi/alass) (GPL-3.0) with [FFmpeg](https://ffmpeg.org). Both are downloaded at first use, not distributed with Napi.
- **Libraries:** built with [Wails](https://wails.io) (MIT), [Cobra](https://github.com/spf13/cobra) (Apache-2.0) and the Go `x/text` and `x/sys` packages (BSD-3-Clause).
