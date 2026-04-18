# bookbeam

Wireless ebook sync from a [Calibre-Web Automated](https://github.com/crocodilestick/Calibre-Web-Automated) instance to a [PocketBook Era Color](https://pocketbook.ch/en-ch/products/pocketbook-era-color) (and likely other PocketBook devices on firmware 6.x).

Native on-device app — no PC, no USB cable. Pulls new and updated EPUBs over Wi-Fi using OPDS, diffs against local state, drops them into the device's library, and triggers a rescan so they appear in the reader.

## Status

Scaffolding — no working sync yet.

## Why

The stock OPDS catalog browser is regionally gated and not present in the UI on German-market PocketBook firmware. The regional lock disables only the stock UI, not the network stack, so a custom app can talk to OPDS endpoints directly. Even where stock OPDS is available, it's manual pull-on-demand; bookbeam is built around scheduled / one-tap sync.

## Build

The repository expects to be built inside a container running [`sunsung/pocketbook-go-sdk`](https://hub.docker.com/r/sunsung/pocketbook-go-sdk) (Go 1.24 + ARMv7 cross-compile toolchain pre-configured).

```sh
go build -o bookbeam.app .
```

The toolchain env is preset: `GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=1`.

## Install on device

Copy `bookbeam.app` to `/mnt/ext1/applications/` on the PocketBook over USB, then launch from the device's Applications menu.

## License

TBD.
