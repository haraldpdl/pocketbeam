# bookbeam

Wireless ebook sync from a [Calibre-Web Automated](https://github.com/crocodilestick/Calibre-Web-Automated) server to a [PocketBook Era Color](https://pocketbook.ch/en-ch/products/pocketbook-era-color) and likely other PocketBook devices on firmware 6.x.

Native on-device app — no PC, no USB cable. Pulls new and updated ebooks over Wi-Fi using OPDS, keeps local state so every sync is an incremental diff, and drops the files straight into the device's library.

## Status

Early but working. The first-run wizard, main sync screen, and settings screen all run under InkView on device. Tested on the PocketBook Era Color (1264x1680). Pending features listed near the bottom.

## Why

The stock OPDS catalog browser is regionally gated; on the German-market firmware of the Era Color it is not even in the Applications menu. The regional lock disables only the stock UI, not the network stack, so a custom app can talk to OPDS endpoints directly. Even where stock OPDS is available it is manual pull-on-demand; bookbeam is built around one-tap sync of the entire library.

## Install

### 1. Build the `.app`

The project expects to be built inside the [`sunsung/pocketbook-go-sdk`](https://hub.docker.com/r/sunsung/pocketbook-go-sdk) Docker image, which ships the PocketBook ARMv7 cross-compile toolchain and Go 1.24.

```sh
docker run --rm -v "$PWD":/app -w /app sunsung/pocketbook-go-sdk:latest \
    go build -ldflags='-s -w' -o bookbeam.app .
```

The stripped binary is about 7.5 MB.

### 2. Sideload

Connect the PocketBook to a computer or phone over USB-C. It shows up as a mass-storage drive. Copy `bookbeam.app` into the `applications/` folder at the root of the device's internal storage. Eject cleanly and disconnect.

The app then appears in the device's Applications menu as `@bookbeam` with a generic icon — that is the PocketBook firmware convention for sideloaded apps (the same `@koreader` + default icon applies to [KOReader](https://github.com/koreader/koreader)). See the note under [Known limitations](#known-limitations) below.

### 3. First-run wizard

Launch bookbeam from the Applications menu. You will be walked through:

1. **Server URL** — e.g. `http://cwa.lan:8083` or `https://cwa.tailnet.example`
2. **Username** — your Calibre-Web Automated login
3. **Password** — same
4. **Testing connection** — the wizard probes the server and validates your credentials before saving

On success, the main sync screen appears. On failure, the wizard shows a specific error (bad URL, wrong credentials, server unreachable, etc.) and offers retry.

## Usage

The main screen has four actions:

- **Sync Now** — connects the Wi-Fi (wakes the radio if asleep), probes the server, then pulls the full OPDS catalog and downloads everything that is new or updated since the last sync. Progress shows the current book counter, a live elapsed-time indicator, and the book title being downloaded. Already-synced books skip instantly.
- **Network** — opens the PocketBook system network dialog so you can switch Wi-Fi networks or re-enable Wi-Fi if you had it off.
- **Settings** — lets you change the server URL, username, or password (re-runs the first-run wizard).
- **Quit** — back to the Applications menu.

Books land under `/mnt/ext1/Books/CWA/<Author>/<Title>.<ext>` and show up in the device's library after the next library refresh.

## Configuration file

For power users only. The wizard writes the config to `/mnt/ext1/system/config/bookbeam.cfg`:

```ini
host     = http://cwa.lan:8083
user     = your-cwa-username
password = your-cwa-password
library  = /mnt/ext1/Books/CWA
state_db = /mnt/ext1/system/config/bookbeam.db
```

You can edit it by hand over USB if you prefer not to go through the on-device wizard.

## Development

A second entry point compiles for amd64 and runs the sync as a plain CLI without InkView, so you can iterate on the core logic without sideloading:

```sh
# dev build for fast iteration on a workstation or in a container
GOOS=linux GOARCH=amd64 GOARM= CC= go build -o bookbeam-amd64 .

# run against a CWA instance
./bookbeam-amd64 -config ./bookbeam.cfg -v
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

- **App name shows as `@bookbeam`** in the PocketBook launcher with a generic icon. This is the PocketBook firmware's default presentation for any sideloaded app and matches KOReader's `@koreader` presentation. Customising it requires editing `/mnt/ext1/system/config/desktop/view.json` and placing BMP icons under `/mnt/ext1/applications/icons/`; that is a per-user polish step, not part of the default install.
- **Password entry is visible** on the on-screen keyboard. The PocketBook InkView keyboard has no masked-input mode exposed through the SDK. Use a stance that blocks onlookers or set a throwaway password in CWA for device use.
- **Single source only.** bookbeam syncs one CWA server at a time. Multiple-library support is a v2 item.
- **Full catalog pull, no filter.** Every book CWA exposes is pulled; no shelf, tag, or search-term filter yet.
- **No delete on remote-removal.** Books deleted from CWA are not removed from the device.
- **Sync cannot be cancelled mid-run.** Interrupting (Back key or force-quit) is safe but leaves a partial download as a `.part` file that the next sync retries cleanly.

Several of these are the target of v2.

## License

MIT. See [LICENSE](LICENSE).
