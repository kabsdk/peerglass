# peerglass requirements

peerglass is a small web UI for rTorrent that shows what every torrent is doing
and why, in the informative style of qBittorrent's interface. It is for
monitoring and for "add this torrent, download it here". It is not for
configuring rTorrent: rTorrent's settings live in `rtorrent.rc`, which is
managed as code in `../ansible-infrastructure`. The line is permanence:
anything that should survive a restart is `rtorrent.rc`'s; the UI may only
make temporary, operational changes (F13).

Requirement IDs (F, S, N) are stable; refer to them in commits and tests.

## 1. Users and context

- One kind of user: an administrator, already authenticated by Authelia in
  front of the UI. peerglass has no login of its own.
- It runs on app01 in the `rtorrent` Compose stack,
  `../ansible-infrastructure/docker-compose/app01.infra.aslot-int.dk/rtorrent/`,
  where it replaced ruTorrent. The stack has three containers: gluetun (VPN
  with kill switch), rTorrent 0.16.27 built from rakshasa's release tarballs
  and sharing gluetun's network, and peerglass.
- rTorrent is reached only through its SCGI Unix socket,
  `/run/rtorrent/rtorrent.sock` inside the containers.
- About 825 torrents, nearly all private, most of them seeding. The UI must
  stay usable at 1,000.
- `/downloads` in the containers is the NFS media share. Labels (`tv`,
  `movies`) are stored in rTorrent's `d.custom1`.

### Behaviour that rtorrent.rc already provides

peerglass must rely on these, not reimplement them:

- A torrent added with a path outside `/downloads/`, or one containing `..`,
  is moved to `/downloads/unsorted/` when it is added.
- When a torrent in `/downloads/incomplete/` finishes, rTorrent moves it by
  label: `tv` and `movies` to their folders, anything else to
  `/downloads/unsorted/`. Torrents added with an explicit path stay put.
- The listen port follows the VPN's forwarded port.

## 2. Functional requirements (first version)

### Torrent list

- **F1.** One table of all torrents with name, size, progress, state, status
  text, download and upload rate, ratio, label, tracker host and date added.
- **F2.** The status text says *why* a torrent is in its state, in words. That
  includes the tracker's message (rTorrent's `d.message`), hash checking,
  "no seeds" and stopped. An icon alone is never enough.
- **F3.** Sort by any column. Filter by state (downloading, seeding, stopped,
  checking, error), by label and by tracker host. A text search on the name.
- **F4.** The table updates itself every 2–3 seconds without a page reload,
  and keeps the sort, filters, search, scroll position and selection.

### Torrent details

- **F5.** Selecting a torrent opens a detail panel with three tabs:
  - **General:** name, hash, path, size, progress, dates, and the message
    in full.
  - **Trackers:** one row per tracker with host, enabled state, success and
    failure counts, seeders and leechers from the last scrape, and the last
    message.
  - **Files:** path, size, progress and priority, read-only.

### Actions

- **F6.** Add torrents from one or more `.torrent` files or a magnet link. The
  options are a label (none, or one of the configured labels), a destination
  path (empty means rTorrent's default, `/downloads/incomplete/`) and "start
  now" (on by default).
- **F7.** Start, stop, recheck and reannounce one or more selected torrents.
  Reannounce is rTorrent's ordinary manual announce (`d.tracker_announce`),
  which still honours a tracker's minimum interval; the forced variant is not
  offered, to stay polite to private trackers.
- **F8.** Change the label of selected torrents to one of the configured
  labels, or none.
- **F9.** Remove selected torrents, keeping their data, after a confirmation.
- **F10.** Remove selected torrents together with their data, behind a
  separate and explicit confirmation. rTorrent deletes the data, not
  peerglass (see S6). This needs a method in `rtorrent.rc`, so it is a
  change in `../ansible-infrastructure`.
- **F11.** When rTorrent is unreachable, every page says so plainly and
  recovers by itself when rTorrent is back.

### Temporary changes and presentation

- **F13.** Global download and upload limits, as a temporary override. The
  header shows the limits in force next to the rates; a small form sets
  them in KiB/s or lifts them. They last until rTorrent restarts, because
  rTorrent never writes its configuration back: `rtorrent.rc` sets the
  limits it starts with, and recurring limits (slower at night) belong there
  too, as a `schedule`.
- **F14.** Per-browser presentation needs no server state. The theme follows
  the system unless the browser picks light or dark; the choice is a cookie,
  rendered by the server so the page does not flash on load.

### Never

- **F12.** No persistent rTorrent configuration. Nothing in the UI changes
  ports, DHT, peer exchange, encryption, peer counts, directories or label
  routing, and nothing it changes outlives an rTorrent restart. F13 is the
  one runtime setting, because it is temporary by nature. peerglass itself
  stores nothing either: its configuration is the environment (N2).

## 3. Security requirements

- **S1. A fixed set of actions.** Each handler maps to one specific rTorrent
  command with validated arguments. Never offer an endpoint that passes
  arbitrary commands or method names to rTorrent. Never call `execute.*`,
  `method.*` or `schedule*` from peerglass. rTorrent's RPC can run shell
  commands, so anyone who can send it arbitrary calls owns the container.
- **S2. Escaping.** Render all HTML with Go's `html/template`. Torrent names,
  file names and tracker messages come from strangers. Never build HTML by
  string concatenation, and never wrap external data in `template.HTML`,
  `template.JS` or `template.URL`. Scripts of our own (S7) may focus,
  position, toggle and ask htmx to fetch; they never build markup from data
  (`innerHTML` and friends), never evaluate strings, and are plain
  JavaScript files in `internal/web/static/`. A test enforces this.
- **S3. Content-Security-Policy.** Send `default-src 'self'`, with no inline
  scripts and no inline event handlers. Allow `style-src 'self'` only.
  Serve every asset from peerglass itself; no CDNs.
- **S4. Requests that change state.** Actions are POST-only. Reject any
  request whose `Origin` (or, failing that, `Referer`) is not the UI's own
  origin. GET requests never change anything.
- **S5. No passkeys.** Private trackers put the user's passkey in the announce
  URL. Show and log only the tracker's scheme and host, never the full URL.
  The same applies to magnet links in logs.
- **S6. Least privilege.** Run as uid/gid 1004 with a read-only root
  filesystem in a minimal image (distroless static or scratch). The UI does not
  mount `/downloads`. It validates destination paths as text (absolute, below
  `/downloads/`, no `..`), and rTorrent's guard is the second line of defence.
- **S7. Dependencies.** The worry is the supply chain. Use the Go standard
  library only. In the browser, the only third-party code is htmx, vendored
  into the repository at a pinned version, with that version and the file's
  SHA-256 recorded next to it and checked by a test. Everything else that
  runs in the browser is plain JavaScript written here: no packages, no
  package manager, no TypeScript, no build step. Docker base images are
  pinned by digest. Any other dependency needs the user's approval first.
- **S8. Input limits.** Cap uploaded `.torrent` size at 16 MB, which is
  rTorrent's XML-RPC limit, and cap request bodies and the number of files per
  add. Accept magnet links only with the `magnet:` scheme.

## 4. Non-functional requirements

- **N1. Speed.** At 1,000 torrents, building the list takes one
  `d.multicall2` call and stays under 200 ms of server time.
- **N2. Configuration.** Use environment variables only, with safe defaults:

  | Variable | Default | Meaning |
  | --- | --- | --- |
  | `PEERGLASS_SOCKET` | `/run/rtorrent/rtorrent.sock` | rTorrent's SCGI socket |
  | `PEERGLASS_LISTEN` | `:8080` | Listen address |
  | `PEERGLASS_ROOT` | `/downloads/` | Allowed destination root |
  | `PEERGLASS_LABELS` | `tv,movies` | Labels offered in the UI |

- **N3. Logging.** Structured logs with `log/slog` to stdout: one line per
  request and per rTorrent error. Log no request bodies, no full tracker
  URLs and no magnet links (S5).
- **N4. Packaging.** A single static binary (`CGO_ENABLED=0`) in a distroless
  image. A version tag (`vX.Y.Z`) makes the release workflow test it and
  publish the public image `ghcr.io/kabsdk/peerglass:X.Y.Z`;
  `../ansible-infrastructure` pins the image by version and digest.
- **N5. Browsers.** Current Firefox and Chromium on desktop. The UI must not
  break on a phone, but it doesn't have to be good there yet.
- **N6. Looks.** Plain, dense and readable, closer to qBittorrent than to a
  dashboard. A light and a dark theme follow the system setting, unless the
  browser picks one (F14).

## 5. Later, not in the first version

- A peers tab, showing address (country), client, flags, rates and progress.
- Editing file priorities.
- Resizable, reorderable columns, remembered per browser.
- Speed graphs.
- Keyboard shortcuts.
- Per-torrent speed limits, rTorrent's way: throttle groups defined in
  `rtorrent.rc` (policy) and a torrent assigned to one at runtime
  (`d.throttle_name.set`), the same split as labels.
- Moving a torrent's data ("set location"), as an `rtorrent.rc` method that
  rTorrent runs, like F10.
- A layout made for phones.

## 6. Decisions

- peerglass replaced ruTorrent, on its port, and is reached as
  `torrent.aslot.dk`.
- The repository and image are public; deployments pin the image by digest.
- F10 is in the first version; the `d.erase_with_data` method is in
  `rtorrent.rc` on app01.
