# napi

Downloads Polish (and English) subtitles from [napiprojekt.pl](https://napiprojekt.pl) for video files and whole folder trees. Windows-first, a single portable exe, no installer.

- **Explorer menu:** right-click a video, several videos, a folder or a folder background → *Pobierz napisy* (configured languages, Polish by default) or *Pobierz napisy (eng)* (English only, from both NapiProjekt and OpenSubtitles). Progress shows in a card in the bottom-right corner (current step and file, overall progress, *Anuluj*); when done it shows the result with actions (*Pokaż plik*, *Otwórz stronę filmu*, *Szczegóły* with a per-file list) and closes itself after a clean success.
- **Re-sync existing subtitles:** right-click one or more `.srt`/`.sub` files → *Synchronizuj napisy*. Each is matched to the video of the same name in its folder (`Film.pol.srt`, `Film.pl.forced.srt` → `Film.mkv`), aligned to its audio and saved in place as `.srt`; the previous file is kept as `.bak`. Command line: `napi sync Film.pol.srt [more…]` (wildcards work in cmd too).
- **Drag and drop** videos or folders onto `napiw.exe`.
- **Settings window:** start `napiw.exe` on its own: languages, format, encoding, naming, OpenSubtitles login with a connection test, the Explorer menu switch.
- **CLI** for batch jobs and scripts: `napi get D:\Filmy E:\Seriale`.
- **Fallback to OpenSubtitles**, including subtitles made for *another release* of the same movie, automatically synced to your file's audio.

## Existing subtitles

Subtitles are named with the language (`Film.pol.srt`, `Film.eng.srt` by default), so Polish and English ones live side by side. Existing files count per language: `Film.pl.srt`, `Film.pol.srt`, `Film.polish.srt` … are Polish; for files without a language in the name (`Film.srt`) napi reads the text and recognises Polish or English (unrecognisable ones count as the first configured language). English subtitles are never blocked by Polish ones and vice versa.

From the Explorer menu, when a video already has subtitles in the requested language the card asks first – for one film *Pobierz nowe* / *Zostaw*, for a batch *Tylko brakujące* / *Wszystkie od nowa*. Replaced files (all existing ones in that language) are renamed to `<name>.bak`. The command line skips such videos unless given `--force`; `"overwrite": true` in the settings replaces without asking.

## Where subtitles come from

For each video, in order (each language from `languages` in turn for the exact steps):

1. **NapiProjekt, exact file** – by MD5 of the first 10 MiB.
2. **OpenSubtitles, exact file** – by OpenSubtitles moviehash (`moviehash_match`).
3. **OpenSubtitles, same movie, other release** – the movie is identified by its IMDb ID (NapiProjekt usually knows which movie a file belongs to even without subtitles, and the hash search often returns it too); failing that, by title and year from the file name. Candidates are ranked: preferred language, human over machine translations, then release name similarity (source, resolution, codec, group), same frame rate, popularity. The best one is downloaded, converted to SRT and aligned to the video's audio with [alass](https://github.com/kaegi/alass) (offsets, cuts/splits and 25 vs 23.976 fps speed differences). If that fails the second candidate is tried.

4. **Nothing found, but NapiProjekt recognises the movie** (the case where the official app shows a cover) – the result names the movie and links its napiprojekt.pl page, which lists subtitles for other versions; the corner card offers to open it. Clicking a version's `napiprojekt:` link there downloads it and syncs it to the waiting video automatically (see below). The lookup only happens after all automatic sources failed.

Series episodes are handled throughout: NapiProjekt reports the series, season and episode for an episode's hash, and OpenSubtitles is then searched for that episode (`parent_imdb_id` + season + episode), falling back to `S01E04` in the file name. Before syncing, a candidate's length is checked against the video's duration (from the MKV/MP4/AVI header), so subtitles for another episode or a much longer cut are skipped without running alass.

### napiprojekt: links

`napi menu install` also registers the `napiprojekt:` URL scheme used on napiprojekt.pl movie pages (for the current user; it overrides the official client's handler, and `napi menu uninstall` restores it). Videos that ended in step 4 are remembered for 14 days in `%LOCALAPPDATA%\napi\pending.json` with their movie and episode. Clicking a link downloads that version and syncs it to the remembered video of the same movie/episode; if none or several match, a file picker asks which video it is for. The same works from the command line: `napi sync Odcinek.mkv napiprojekt:<hash>`.

Synced subtitles are marked in the output (`[OpenSubtitles, synced from <release>]`, `source` column in reports) and in the Explorer summary, because a different cut can still leave a scene off.

Steps 2–3 need OpenSubtitles credentials; without them napi behaves as NapiProjekt-only. Step 3 can be turned off with `"sync": false`. A downloaded subtitle counts against the account's daily limit; when it's reached napi stops using OpenSubtitles for the rest of the run.

Folders named `Featurettes`, `Extras`, `Trailers`, `Behind The Scenes`, `Deleted Scenes`, `Interviews`, `Samples` … are skipped when scanning (select such files directly if you want them), and step 3 never searches by a title without a year.

## OpenSubtitles setup

1. Create a free account at [opensubtitles.com](https://www.opensubtitles.com) and an API consumer at <https://www.opensubtitles.com/consumers> (gives the API key).
2. `napi config init` and fill in `napi.json`:
   ```json
   "openSubtitles": { "apiKey": "…", "username": "…", "password": "…", "userAgent": "napi v0.2.0" }
   ```
   The login token is cached in `%LOCALAPPDATA%\napi` for 20 hours.
3. Check with `napi search Film.mkv`, which lists the ranked candidates without downloading.

## Sync tool

The first time a sync is needed napi downloads alass 2.0.0 for Windows (26 MB, bundles ffmpeg; SHA-256 pinned) into `%LOCALAPPDATA%\napi\tools`. Set `alassPath` to use your own. A sync reads the movie's whole audio track: about 50 s for a 2-hour 4 GB file on a network drive.

## Install

1. Unzip `napi.exe` and `napiw.exe` into a permanent folder, e.g. `%LOCALAPPDATA%\napi`.
2. Double-click `napiw.exe` and switch on *Menu Eksploratora i linki napiprojekt:*, or run `napi menu install`. Registers for the current user only; no admin rights needed.
   On Windows 11 the entry is under *Show more options* (Shift+F10).
3. Remove with `napi menu uninstall` (or the same switch in the settings window).

## CLI

```
napi Film.mkv                         # same as "napi get Film.mkv"
napi get D:\Filmy E:\Seriale -l PL,ENG -r raport.csv
napi get . --no-recursive --force     # this folder only, overwrite existing
napi hash Film.mkv                    # NapiProjekt hash + detected frame rate
napi search Film.mkv                  # OpenSubtitles candidates, ranked (needs API key)
napi account                          # OpenSubtitles login check, downloads left today
napi sync Film.mkv inne-napisy.txt    # align any subtitles to the video's audio → Film.srt
napi sync Film.mkv napiprojekt:<hash> # same, for a version linked on napiprojekt.pl
napi config                           # show settings file and values
napi config init [--portable]         # write napi.json (portable: next to the exe)
```

| flag | default | |
|---|---|---|
| `-l, --lang` | `PL` | languages in order of preference: `PL`, `ENG`, `PL,ENG` |
| `--format` | `srt` | `srt` converts every format to SubRip; `original` keeps what the server has (`.txt`) |
| `-e, --encoding` | `utf8bom` | `utf8bom`, `utf8`, or `cp1250` for old TVs and players |
| `-n, --naming` | `iso639-2` | file names: `iso639-2` (`Film.pol.srt`, `Film.eng.srt`), `iso639-1` (`Film.pl.srt`), `none` (`Film.srt`) |
| `-f, --force` | off | replace existing subtitles in that language; the old files are kept as `.bak` |
| `--no-recursive` | off | don't descend into subfolders |
| `-j, --jobs` | `4` | videos in parallel; API requests are spaced at least 200 ms apart |
| `--fps` | `23.976` | frame rate for MicroDVD conversion when the video's can't be read |
| `-r, --report` | | write a CSV with one row per video |

Exit code: `0` all videos have subtitles, `2` some weren't found, `1` errors.

The Explorer menu uses the same settings (`napi.json`) and writes the last run's report to `%LOCALAPPDATA%\napi\ostatnie.csv`.

## How it works

- **Hash:** MD5 of the first 10 MiB of the video. `mode=2` with `downloaded_cover_id=<hash>` returns the movie the hash is associated with (title, year, IMDb link). The API's `mode` is a bitmask (1 subtitles, 2 movie info, 0x200 upload, 0x800 availability check …); there is no call listing a movie's other subtitle versions, and the napiprojekt.pl website that shows them is behind a Cloudflare challenge.
- **API:** `POST https://napiprojekt.pl/api/api-napiprojekt3.php` with `mode=17`, the hash, `downloaded_subtitles_lang` and `downloaded_subtitles_txt=1`. Subtitles come back base64-encoded in XML. The server only answers whitelisted client names; `client` in `napi.json` defaults to `NapiProjektPython`.
- **Charset:** UTF-8, windows-1250 or ISO-8859-2, detected per file.
- **Formats:** MicroDVD, MPL2, TMPlayer and SRT are parsed; italics/bold become `<i>`/`<b>`.
  MicroDVD is timed in frames, so its frame rate comes from the subtitle's own `{1}{1}23.976` header, then the video's MKV/MP4/AVI header, then `ffprobe` if it's on PATH, then `--fps`.
- **Multi-select:** Explorer starts one process per selected file. Each drops its path into `%TEMP%\napi-queue`; the one holding a named mutex processes the whole queue and shows a single summary.

## Interface

`napiw.exe` is a [Wails](https://wails.io) v2 app: Go backend, the HTML/CSS/JS frontend in `internal/gui/web` is embedded (no npm build step) and rendered by the Edge WebView2 runtime that ships with Windows 10/11. The corner card is a frameless tool window (no taskbar button) with an acrylic backdrop and Windows 11 rounded corners, placed above the taskbar in physical pixels; the settings window uses the Mica backdrop. Both follow the system light/dark theme. The console `napi.exe` doesn't use WebView2.

## Build

Requires Go 1.24+. Cross-compiles from Linux/WSL (`napiw` needs the `desktop,production` build tags, which `build.sh` sets):

```
./build.sh 0.1.0        # dist/napi.exe, dist/napiw.exe, dist/napi-0.1.0-windows-amd64.zip
go test ./...           # NAPI_LIVE=1 also queries napiprojekt.pl
```
