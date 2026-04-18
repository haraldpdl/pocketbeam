# bookbeam

Wireless ebook sync from a [Calibre-Web Automated](https://github.com/crocodilestick/Calibre-Web-Automated) instance to a [PocketBook Era Color](https://pocketbook.ch/en-ch/products/pocketbook-era-color) (and likely other PocketBook devices on firmware 6.x).

Native on-device app — no PC, no USB cable. Pulls new and updated EPUBs over Wi-Fi using OPDS, diffs against local state, drops them into the device's library, and triggers a rescan so they appear in the reader.

## Status

Scaffolding — no working sync yet.

## Why

The stock OPDS catalog browser is regionally gated and not present in the UI on German-market PocketBook firmware. The regional lock disables only the stock UI, not the network stack, so a custom app can talk to OPDS endpoints directly. Even where stock OPDS is available, it's manual pull-on-demand; bookbeam is built around scheduled / one-tap sync.

## Configuration

A flat `key = value` file. Default path on the device: `/mnt/ext1/system/config/bookbeam.cfg`.

```ini
host     = http://cwa.lan:8083
user     = your-cwa-username
password = your-cwa-password
library  = /mnt/ext1/Books/CWA
state_db = /mnt/ext1/system/config/bookbeam.db
```

## Build

The repository expects to be built inside the [`sunsung/pocketbook-go-sdk`](https://hub.docker.com/r/sunsung/pocketbook-go-sdk) image (Go 1.24 + ARMv7 cross-compile toolchain).

```sh
# device build (env preset by the image)
go build -o bookbeam.app .

# dev build for testing on the build host
GOOS=linux GOARCH=amd64 GOARM= CC= go build -o bookbeam-amd64 .
```

CGO is enabled in both cases (SQLite via `mattn/go-sqlite3`).

## Run

```sh
bookbeam                                  # uses default config path
bookbeam -config /path/to/bookbeam.cfg    # explicit config
bookbeam -v                               # log every book processed
```

## Test

```sh
go test ./...
```

## Install on device

Copy `bookbeam.app` to `/mnt/ext1/applications/` on the PocketBook over USB, then launch from the device's Applications menu.

## License

MIT. See [LICENSE](LICENSE).
