# pocketbeam

Wireless ebook sync from any OPDS server or WebDAV share to a [PocketBook Era Color](https://pocketbook.ch/en-ch/products/pocketbook-era-color) and likely other PocketBook devices on firmware 6.x. Tested against [Calibre-Web Automated](https://github.com/crocodilestick/Calibre-Web-Automated) (with shelf-filter and single-request "all books" fast paths) and against Nextcloud / Synology / ownCloud via WebDAV.

Native on-device app, no PC, no USB cable. Pulls new and updated ebooks over Wi-Fi, keeps local state so every sync is an incremental diff, and drops the files straight into the device's library.

## Status

Early but working. The first-run wizard, main sync screen, settings, OPDS feed picker (nested), and WebDAV directory picker all run under InkView on device. Tested on the PocketBook Era Color (1264x1680). Pending features listed near the bottom.

## Why

The stock OPDS catalog browser is regionally gated; on the German-market firmware of the Era Color it is not even in the Applications menu. The regional lock disables only the stock UI, not the network stack, so a custom app can talk to OPDS endpoints directly. Even where stock OPDS is available it is manual pull-on-demand; pocketbeam is built around one-tap sync of the entire library.

## Install

### 1. Build the `.app`

The project expects to be built inside the [`sunsung/pocketbook-go-sdk`](https://hub.docker.com/r/sunsung/pocketbook-go-sdk) Docker image, which ships the PocketBook ARMv7 cross-compile toolchain and Go 1.24.

```sh
docker run --rm -v "$PWD":/app -w /app sunsung/pocketbook-go-sdk:latest \
    go build -ldflags='-s -w' -o pocketbeam.app .
```

The stripped binary is about 8 MB.

### 2. Sideload

Connect the PocketBook to a computer or phone over USB-C. It shows up as a mass-storage drive. Copy `pocketbeam.app` into the `applications/` folder at the root of the device's internal storage. Eject cleanly and disconnect.

The app then appears in the device's Applications menu as `@pocketbeam` with a generic icon. That is the PocketBook firmware convention for sideloaded apps (the same `@koreader` + default icon applies to [KOReader](https://github.com/koreader/koreader)). See the note under [Known limitations](#known-limitations) below.

### 3. First-run wizard

Launch pocketbeam from the Applications menu. You will be walked through:

1. **Server type** — Calibre-Web / OPDS, or WebDAV / Nextcloud
2. **Server URL** — e.g. `http://library.lan:8083` for OPDS, or `https://nc.example.com/remote.php/dav/files/alice` for WebDAV
3. **Username** and **password** — your server login
4. **Testing connection** — the wizard probes the server and validates your credentials before saving

On success, the main sync screen appears. On failure, the wizard shows a specific error (bad URL, wrong credentials, server unreachable, etc.) and offers retry.

For OPDS servers, pocketbeam auto-detects whether the endpoint is Calibre-Web / Calibre-Web Automated and enables a fast path when it is. Against other OPDS servers (Calibre's built-in content server, COPS, Audiobookshelf's OPDS feed, etc.) it falls back to a generic recursive walker that navigates the catalog's subsections; all books still sync, just via more HTTP requests.

## Usage

The main screen has four actions:

- **Sync Now** — connects the Wi-Fi (wakes the radio if asleep), probes the server, then pulls the configured catalog (all books by default, or a specific shelf / folder / subsection if you set one) and downloads everything that is new or updated since the last sync. Progress shows the current book counter, a live elapsed-time indicator, and the book title being downloaded. Already-synced books skip instantly.
- **Network** — opens the PocketBook system network dialog so you can switch Wi-Fi networks or re-enable Wi-Fi if you had it off.
- **Settings** — change the server URL, username, or password, or pick a filter. For OPDS servers the filter picker drills through the catalog's subsections; tap "Sync this level" at any feed to sync that branch (e.g. a specific author, tag, or series on Calibre). For WebDAV the picker drills through server directories the same way. A typical CWA workflow is to create a `to-pocketbook` shelf and pick it as the filter.
- **Quit** — back to the Applications menu.

Books land under `/mnt/ext1/Books/CWA/<Author>/<Title>.<ext>` (OPDS) or `/mnt/ext1/Books/WebDAV/<Author>/<Title>.<ext>` and show up in the device's library after the next library refresh.

## Configuration file

For power users only. The wizard writes the config to `/mnt/ext1/system/config/pocketbeam.cfg`:

```ini
backend     = opds
host        = http://cwa.lan:8083
user        = your-cwa-username
password    = your-cwa-password
library     = /mnt/ext1/Books/CWA
state_db    = /mnt/ext1/system/config/pocketbeam.db
filter_href = /opds/shelf/3
filter_name = to-pocketbook
```

For WebDAV:

```ini
backend  = webdav
host     = https://nc.example.com/remote.php/dav/files/alice
user     = alice
password = hunter2
library  = /mnt/ext1/Books/WebDAV
state_db = /mnt/ext1/system/config/pocketbeam.db
path     = /Books/Fiction
```

You can edit either by hand over USB if you prefer not to go through the on-device wizard.

## Development

A second entry point compiles for amd64 and runs the sync as a plain CLI without InkView, so you can iterate on the core logic without sideloading:

```sh
# dev build for fast iteration on a workstation or in a container
GOOS=linux GOARCH=amd64 GOARM= CC= go build -o pocketbeam-amd64 .

# run against a server
./pocketbeam-amd64 -config ./pocketbeam.cfg -v
```

Unit tests:

```sh
go test ./...
```

Live tests against a real CWA (gated behind the `live` build tag):

```sh
go test -tags=live -run TestLive
```

## Known limitations

- **App name shows as `@pocketbeam`** in the PocketBook launcher with a generic icon. This is the PocketBook firmware's default presentation for any sideloaded app and matches KOReader's `@koreader` presentation. Customising it requires editing `/mnt/ext1/system/config/desktop/view.json` and placing BMP icons under `/mnt/ext1/applications/icons/`; that is a per-user polish step, not part of the default install.
- **Password entry is visible** on the on-screen keyboard. The PocketBook InkView keyboard has no masked-input mode exposed through the SDK. Use a stance that blocks onlookers or set a throwaway password for device use.
- **Single source only.** pocketbeam syncs one server at a time. Multiple-library support is a v2 item.
- **No delete on remote-removal.** Books deleted from the server are not removed from the device.
- **Sync cannot be cancelled mid-run.** Interrupting (Back key or force-quit) is safe but leaves a partial download as a `.part` file that the next sync retries cleanly.

## License

MIT. See [LICENSE](LICENSE).
