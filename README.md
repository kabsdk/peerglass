# peerglass

A small web UI for [rTorrent](https://github.com/rakshasa/rtorrent) that shows
what every torrent is doing, and why. It is built to be minimal, because a
torrent client's control interface is a dangerous thing to put on a network.

**peerglass manages torrents, not the torrent client.** You use it to watch
torrents, add them ("download this, put it there"), start, stop, label and
remove them. You do not use it to configure rTorrent: ports, DHT, encryption,
directories and where finished downloads go belong in `rtorrent.rc`, kept as
code. That is why there is no settings page. The one runtime control is a
temporary global speed limit, which ends when rTorrent restarts.

## What it does

- One dense table of every torrent: size, progress, rates, ratio, label,
  tracker and added date. The status says *why* a torrent is in its state:
  the tracker's message in full, hash checking, no seeds, stopped.
- Filters in a sidebar by state, label and tracker, with counts, plus a
  search. The table refreshes itself every two seconds, sending only the rows
  that changed, so it stays quick at 1,000 torrents.
- Details for one torrent: general, trackers and files.
- Selection like a file manager (click, Ctrl-click, Shift-click, Ctrl+A) and
  a right-click menu. Actions apply to the selection: start, stop, recheck,
  reannounce, set label, remove, and remove with data.
- Adding `.torrent` files or a magnet link, with a label and a destination.
- A temporary global download and upload limit, and a light or dark theme.

## Minimal on purpose

rTorrent's RPC interface can run shell commands. Whoever can send it
arbitrary calls owns the machine it runs on. peerglass is designed around
that fact:

- **A fixed set of actions.** The browser never talks to rTorrent. Each
  button maps to one specific rTorrent command with validated arguments.
  There is no generic RPC endpoint, and peerglass never calls `execute.*`,
  `method.*` or `schedule*`.
- **No settings.** A UI that cannot change rTorrent's configuration cannot
  weaken it, and the configuration stays reviewable as code.
- **A tiny supply chain.** The server uses the Go standard library only;
  `go.mod` has no dependencies. In the browser, the only third-party code is
  [htmx](https://htmx.org), vendored at a pinned version and checked against
  its recorded SHA-256 by a test. The rest is about 240 lines of plain
  JavaScript written here, with no npm, no packages and no build step. Docker
  base images are pinned by digest, and the release workflow uses GitHub's
  own checkout, pinned by commit, and nothing else.
- **Server-rendered and escaped.** Every page is rendered with Go's
  `html/template`, because torrent names and tracker messages come from
  strangers. A strict Content-Security-Policy allows only peerglass's own
  files, with no inline scripts or styles, and a test keeps our scripts from
  ever building markup.
- **Safe requests.** Anything that changes state is a POST from the UI's own
  origin; everything else is read-only.
- **No passkeys.** Private trackers put a passkey in the announce URL.
  peerglass reduces tracker URLs to scheme and host before they reach a page
  or a log line.
- **Least privilege.** A static binary in a distroless image, running as an
  unprivileged user on a read-only filesystem. It does not mount your
  downloads: when you remove a torrent with its data, rTorrent deletes the
  files, not peerglass. It stores nothing.
- **No login of its own.** Run it behind an authenticating reverse proxy,
  such as Authelia. Never expose it directly.

## Running it

peerglass needs rTorrent 0.16 and access to its SCGI Unix socket, nothing
else. It is configured through the environment:

| Variable | Default | Meaning |
| --- | --- | --- |
| `PEERGLASS_SOCKET` | `/run/rtorrent/rtorrent.sock` | rTorrent's SCGI socket |
| `PEERGLASS_LISTEN` | `:8080` | Listen address |
| `PEERGLASS_ROOT` | `/downloads/` | The only tree new torrents may be saved into |
| `PEERGLASS_LABELS` | `tv,movies` | Labels offered in the UI |
| `TZ` | UTC | Time zone for dates in the UI |

```sh
docker build -t peerglass .
docker run -d --name peerglass --read-only --user 1004:1004 \
  -v /path/to/rtorrent/run:/run/rtorrent \
  -p 127.0.0.1:8080:8080 peerglass
```

The user must be allowed to use rTorrent's socket. A few features rely on
`rtorrent.rc`:

- the **Added** column reads a custom value, `addtime`, set when a torrent is
  added;
- **Remove with data** calls `d.erase_with_data`, a method you define, so
  that rTorrent does the deleting and decides which paths it may delete;
- the details show where a torrent will move when it finishes, if
  `d.finished_dir` is defined.

Without them everything else works: the added date shows as unknown, the
destination is left out, and Remove with data reports rTorrent's error.
[docs/rtorrent.md](docs/rtorrent.md) has the details.

## Releasing

Push a version tag on `main`, such as `v0.2.0`. The release workflow
([.github/workflows/release.yml](.github/workflows/release.yml)) runs the
checks, builds the image and publishes `ghcr.io/kabsdk/peerglass:0.2.0`; its
summary shows the image line to deploy, pinned by digest. Our deployment is one
Compose stack in `ansible-infrastructure` with gluetun, rTorrent and peerglass;
compose recreates only the containers that changed, so updating the UI never
restarts rTorrent.

## Development

```sh
make check   # gofmt, go vet and the tests; must pass before every commit
make build   # bin/peerglass, a static binary
```

[docs/testing.md](docs/testing.md) shows how to run a local rTorrent with
throwaway torrents. Never point peerglass or its tests at an rTorrent that
matters to you.

- [docs/requirements.md](docs/requirements.md): what peerglass does, and must
  never do
- [docs/rtorrent.md](docs/rtorrent.md): the rTorrent commands it uses, and
  their quirks
- [docs/testing.md](docs/testing.md): how it is tested
- [AGENTS.md](AGENTS.md): working rules for coding agents
