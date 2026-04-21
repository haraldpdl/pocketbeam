# pocketbeam

Wireless ebook sync from any OPDS server or WebDAV share to a [PocketBook Era Color](https://pocketbook.ch/en-ch/products/pocketbook-era-color) and likely other PocketBook devices on firmware 6.x. Tested against [Calibre-Web Automated](https://github.com/crocodilestick/Calibre-Web-Automated) (with shelf-filter and single-request "all books" fast paths) and against Nextcloud / Synology / ownCloud via WebDAV.

Native on-device app, no PC, no USB cable. Pulls new and updated ebooks over Wi-Fi, keeps local state so every sync is an incremental diff, and drops the files straight into the device's library.

## Status

Early but working. The first-run wizard, main sync screen, settings, OPDS feed picker (nested + multi-select), WebDAV directory picker, and multi-server profile switcher all run under InkView on device. Primary development happens on a PocketBook Era Color (1264x1680); the layout scales for smaller panels (Touch HD, Touch Lux 5, InkPad X) via a three-tier width heuristic. Pending features listed near the bottom.

## Why

The stock OPDS catalog browser is regionally gated; on the German-market firmware of the Era Color it is not even in the Applications menu. The regional lock disables only the stock UI, not the network stack, so a custom app can talk to OPDS endpoints directly. Even where stock OPDS is available it is manual pull-on-demand; pocketbeam is built around one-tap sync of the entire library.

## Install

### 1. Build the `.app`

The project expects to be built inside the [`sunsung/pocketbook-go-sdk`](https://hub.docker.com/r/sunsung/pocketbook-go-sdk) Docker image, which ships the PocketBook ARMv7 cross-compile toolchain and Go 1.24.

```sh
docker run --rm -v "$PWD":/app -w /app sunsung/pocketbook-go-sdk:latest \
    go build -ldflags="-s -w -X main.version=$(git describe --tags --always --dirty)" \
    -o pocketbeam.app .
```

The stripped binary is about 8 MB. The `-X main.version=...` flag bakes the
current git tag into the binary; the on-device updater compares that against
the latest Gitea release to decide whether to offer an upgrade. A bare
`go build` without the flag leaves the version as `dev`, which the updater
treats as older than any tagged release (so the updater is always willing
to replace a dev build with a real release).

### 2. Sideload

Connect the PocketBook to a computer or phone over USB-C. It shows up as a mass-storage drive. Copy `pocketbeam.app` into the `applications/` folder at the root of the device's internal storage. Eject cleanly and disconnect.

The app then appears in the device's Applications menu as `@pocketbeam` with a generic icon. That is the PocketBook firmware convention for sideloaded apps (the same `@koreader` + default icon applies to [KOReader](https://github.com/koreader/koreader)). See the note under [Known limitations](#known-limitations) below.

### 3. First-run wizard

Launch pocketbeam from the Applications menu. You will be walked through:

1. **Server type**: Calibre-Web / OPDS, or WebDAV / Nextcloud
2. **Server URL**: e.g. `http://library.lan:8083` for OPDS, or `https://nc.example.com/remote.php/dav/files/alice` for WebDAV
3. **Username** and **password**: your server login
4. **Testing connection**: the wizard probes the server and validates your credentials before saving

On success, the main sync screen appears. On failure, the wizard shows a specific error (bad URL, wrong credentials, server unreachable, etc.) and offers retry.

For OPDS servers, pocketbeam auto-detects whether the endpoint is Calibre-Web / Calibre-Web Automated and enables a fast path when it is. Against other OPDS servers (Calibre's built-in content server, COPS, Audiobookshelf's OPDS feed, etc.) it falls back to a generic recursive walker that navigates the catalog's subsections; all books still sync, just via more HTTP requests.

## Usage

The main screen has four actions:

- **Sync Now**: connects the Wi-Fi (wakes the radio if asleep), probes the server, then pulls the configured catalog (all books by default, or one or more filters / folders that you picked) and downloads everything that is new or updated since the last sync. Progress shows the current book counter, a live elapsed-time indicator, and the book title being downloaded. Already-synced books skip instantly. While a sync is in flight the same button reads **Stop**; tapping it halts the run cleanly (books already downloaded stay, the in-flight `.part` file is left for the next sync's stale-sweep to remove).
- **Network**: opens the PocketBook system network dialog so you can switch Wi-Fi networks or re-enable Wi-Fi if you had it off.
- **Settings**: change the server URL / credentials, pick sync filters, toggle delete-missing, or switch between server profiles. Four buttons:
    - **Change server info**: re-runs the wizard for the active profile.
    - **Change sync filter / folder**: opens the picker. For OPDS, drills through the catalog's subsections; tapping a row descends, "Add this level" accumulates a selection, and "Done" saves the set. Empty subsections (opds:count = 0) are hidden, long lists paginate with Prev / Next. For WebDAV it drills through server directories the same way.
    - **Delete missing**: opt-in toggle. When on, every sync ends with a confirmation prompt listing books no longer on the server; tap Delete or Keep.
    - **Profile: &lt;name&gt;**: opens the profile list. Tap any profile to switch, "Add new server" to create another (one device can sync from a home CWA, a friend's Nextcloud, and a public OPDS server, each as its own profile), or "Delete active profile" to remove one.
- **Quit**: back to the Applications menu.

Books land under `/mnt/ext1/Books/CWA/<Author>/<Title>.<ext>` (OPDS) or `/mnt/ext1/Books/WebDAV/<Author>/<Title>.<ext>` and show up in the device's library after the next library refresh.

## Configuration file

For power users only. The wizard writes the config to `/mnt/ext1/system/config/pocketbeam.cfg`. The file uses `[section]` headers so multiple server profiles live side-by-side:

```ini
active   = home
state_db = /mnt/ext1/system/config/pocketbeam.db

[home]
backend     = opds
host        = http://cwa.lan:8083
user        = your-cwa-username
password    = your-cwa-password
library     = /mnt/ext1/Books/CWA
filter_href = /opds/shelf/3
filter_name = to-pocketbook
filter_href = /opds/category/tag/12
filter_name = Fantasy
delete_missing = on

[nas]
backend  = webdav
host     = https://nc.example.com/remote.php/dav/files/alice
user     = alice
password = hunter2
library  = /mnt/ext1/Books/WebDAV
path     = /Books/Fiction
```

`filter_href` / `filter_name` can repeat to sync more than one feed per profile; books are deduped by UUID across the union. `state_db` is global and shared by every profile (identities don't collide across real catalogs).

You can edit this by hand over USB if you prefer not to go through the on-device wizard.

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
- **Interrupting a sync leaves a partial download** as a `.part` file. The next sync's stale-sweep removes anything older than one hour, so no manual cleanup is needed.
- **Library rescan not triggered automatically.** After sync, new covers and titles appear when the PocketBook library app is opened; pocketbeam does not force an immediate rescan because the stock `scanner.app` steals foreground focus and makes pocketbeam look frozen.
- **Only tested on firmware 6.x.** Older firmware may lack the `NetMgrPing` keepalive API; sync will still work but the radio may drop during long runs.

## Updates and privacy

pocketbeam checks for new releases so you don't have to re-sideload manually. The check runs:

- **Once at first launch** after sideloading (there's no prior check timestamp, so the app hits the release endpoint within a few seconds of startup).
- **Once a week thereafter**, gated by the `last_update_check` timestamp stored in the local state DB.
- **On demand** when you tap the version line in Settings.

The update check is a single HTTPS GET to the release endpoint (dev builds: Gitea on your LAN; public builds: a project-hosted JSON endpoint). The request includes a `User-Agent` header of the form `pocketbeam/<version> (<device-model>; <hardware-type>; fw <firmware>; <width>x<height>)` so the release backend can see which PocketBook models and firmware versions are running which release. No personal data, no library contents, no identifiers beyond what you'd leak to any HTTPS server you visit. The backend logs whatever its web-server access log records, which includes your IP address at the time of the request.

To disable all automatic update checks, tap the version line in Settings to open the update screen, then tap the **Automatic weekly checks: on** row to flip it off. Manual "Check for updates" remains available on that screen even when automatic checks are disabled. Power users can also set `check_updates = off` at the top of `pocketbeam.cfg`.

The app does **no other telemetry**: no usage stats, no crash pings, no version beacons outside the update check. The only outbound HTTP traffic pocketbeam ever initiates is (a) OPDS / WebDAV requests to your configured server, (b) the update check described above, (c) the release binary download when you tap Install. KOReader has the same posture minus the weekly check.

## License

MIT. See [LICENSE](LICENSE).
