# Testing

Tests should give confidence quickly and stay out of the way. `make check`
(gofmt, `go vet`, `go test ./...`) runs in seconds, works offline, and must
pass before every commit.

## What to test

1. **The rTorrent client** (`internal/rtorrent`): test SCGI framing,
   XML-RPC encoding and decoding, and fault handling. Write table-driven
   tests against responses saved in `testdata/` that were captured from a
   real rTorrent. Scrub every captured response: no real tracker URLs,
   passkeys or magnet links.
2. **Interpreting values:** test how raw rows become what the UI shows,
   including status text (F2), state, progress, the ratio (stored times 1000)
   and tracker host extraction (S5).
3. **Handlers** (`internal/web`): use `httptest` against a fake rTorrent,
   which is a small SCGI server on a temporary Unix socket that returns
   canned responses and records the calls it receives. For actions, check
   that rTorrent received exactly the expected command and arguments, not
   just the HTTP status.
4. **Security** (S1–S8) gets its own regression tests:
   - a torrent name and a tracker message containing `<script>`, quotes and
     `{{` appear escaped in the page;
   - an action with a foreign or missing `Origin` is rejected, and so are
     GET requests to action URLs;
   - destination paths outside the root, relative paths, paths with `..`,
     and unknown labels are rejected before anything reaches rTorrent;
   - values with commas, quotes and backslashes are quoted correctly in
     load commands;
   - a full announce URL with a passkey never appears in a page or a log
     line;
   - pages carry the Content-Security-Policy header.

## What not to do

- No browser automation or end-to-end UI tests for now. Check the UI by hand
  (see the end of this page).
- No snapshot tests of whole pages. Assert the parts that matter: a row is
  present, a value is escaped, a header is set.
- No mocking of the standard library. Fake rTorrent at the socket, the one
  boundary that matters.
- No coverage target. The client, the action validation and the escaping
  must be covered; the rest only as far as it helps.

## Against a real rTorrent (optional)

`go test -tags integration ./...` runs extra tests against a real rTorrent
listening on a socket in a temporary folder, using the image that
`../ansible-infrastructure` builds
(`docker-compose/app01.infra.aslot-int.dk/rtorrent/build/rtorrent`). These tests:

- use throwaway torrents made in the test, with no trackers, added stopped;
- use a fresh info-hash for every add (see the erase quirk in
  [rtorrent.md](rtorrent.md));
- clean up their container and folders;
- never connect to the production rTorrent on app01.

They are for checking the client against a new rTorrent version, not for
every commit.

## By hand, before a release

Use a local rTorrent with throwaway torrents, or the trial deployment once
it exists. A local rTorrent is the production image with the production
`rtorrent.rc` minus its VPN block, run as your own user so you can reach the
socket:

```sh
RC=../ansible-infrastructure/docker-compose/app01.infra.aslot-int.dk/rtorrent
docker build --build-arg RTORRENT_VERSION=0.16.27 -t local/rtorrent:0.16.27 $RC/build/rtorrent
mkdir -p dev/config dev/run dev/downloads/{tv,movies,incomplete,unsorted}
sed '/^# Follow the port the VPN forwards/,$d' $RC/config/rtorrent/rtorrent.rc > dev/config/rtorrent.rc
docker run -d --name rtorrent-dev --user $(id -u):$(id -g) --init \
  -v $PWD/dev/config:/config -v $PWD/dev/run:/run/rtorrent -v $PWD/dev/downloads:/downloads \
  local/rtorrent:0.16.27
# Unix socket paths are short; run peerglass from the socket's directory.
(cd dev/run && PEERGLASS_SOCKET=rtorrent.sock PEERGLASS_LISTEN=127.0.0.1:8080 ../../bin/peerglass)
```

Linux distribution ISOs (Debian publishes `.torrent` files) make good
throwaway torrents: public, legal and well seeded. For how the UI feels at
production size, load a few hundred throwaway multi-file torrents without
trackers (a short script over the socket) and watch a headless Chromium:
what matters is the time from a click or keystroke to the DOM change, not
the server time. Then check:

- [ ] The list loads and updates itself; sort, filter and selection survive
  the updates.
- [ ] A torrent with a tracker error shows the message in the list and in its
  details.
- [ ] Adding works with no label and with each label. A path outside
  `/downloads` is refused.
- [ ] Selection behaves like a file manager: click selects one row and shows
  its details, Ctrl-click toggles, Shift-click extends, the checkbox toggles
  without changing the details, and a right-click on an unselected row
  selects it alone before the menu opens. Nothing of this waits for a poll.
- [ ] Start, stop, recheck, reannounce and remove do what they say, on one
  torrent and on several, from the toolbar and from the right-click menu.
- [ ] Speed limits set from the header show next to the rates at once, can
  be lifted with "Unlimited", and are gone after an rTorrent restart.
- [ ] The theme button cycles System, Light and Dark, and a reload keeps the
  choice without a flash.
- [ ] Stopping rTorrent shows "rTorrent unavailable", and the page recovers
  once rTorrent is back.
