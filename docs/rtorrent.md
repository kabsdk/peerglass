# Talking to rTorrent

These are notes on rTorrent's API as peerglass uses it. They were gathered
while setting up the stack against rTorrent 0.16.27. Command names do change
within 0.16.x (0.16.18 renamed the listen-port commands), so before relying on
a command not listed here, check that it exists with `system.listMethods`. Do
that check again whenever `torrent_rtorrent_version` changes in
`../ansible-infrastructure`.

## Transport

peerglass connects to rTorrent's SCGI Unix socket and speaks XML-RPC over it.

1. Send a netstring header followed by the XML-RPC body:
   `<len>:CONTENT_LENGTH\0<body length>\0SCGI\x001\0,<body>`.
2. Read until rTorrent closes the connection. The reply has HTTP-style
   headers, a blank line (`\r\n\r\n`), then the XML-RPC response.
3. Use one connection per call. rTorrent handles calls one at a time on its
   main thread, so keep calls few and batched (`d.multicall2`).

Unix socket paths are limited to about 108 bytes. Tests that create sockets in
long temporary paths should connect relative to the socket's directory.

## Reading

Global commands take an empty target string (`""`) as their first parameter.
Per-torrent commands take the info-hash, in upper case as rTorrent reports it.

| Purpose | Call |
| --- | --- |
| Version | `system.client_version` |
| All torrents, one call | `d.multicall2 "" main d.hash= d.name= d.size_bytes= d.completed_bytes= d.state= d.is_active= d.is_open= d.complete= d.is_hash_checking= d.hashing= d.chunks_hashed= d.size_chunks= d.down.rate= d.up.rate= d.ratio= d.custom1= d.directory= d.message= d.custom=addtime d.timestamp.finished= d.peers_connected= d.peers_complete= d.peers_accounted= d.is_multi_file= d.is_private= t.multicall=,t.url=,t.is_enabled=` |
| One torrent, same fields | `system.multicall` with one `d.*` call per field (`d.custom <hash> addtime`, `t.multicall <hash> "" t.url= t.is_enabled=`). Each entry's struct must name `methodName` before `params`, or rTorrent faults. |
| List and limits, one request | `system.multicall` of the `d.multicall2` above, `throttle.global_down.max_rate ""` and `throttle.global_up.max_rate ""`. `d.multicall2` works inside `system.multicall`, so a poll is one request. |
| Trackers of one torrent | `t.multicall <hash> "" t.url= t.is_enabled= t.success_counter= t.failed_counter= t.scrape_complete= t.scrape_incomplete= t.activity_time_last= t.scrape_time_last=` |
| Files of one torrent | `f.multicall <hash> "" f.path= f.size_bytes= f.completed_chunks= f.size_chunks= f.priority=` |

The last `d.multicall2` field is a nested call: `t.multicall=,t.url=,t.is_enabled=`
returns one `[url, enabled]` pair per tracker inside each row, so the tracker
column needs no call per torrent.

The returned values need some interpretation:

- **Ratio:** `d.ratio` is the ratio times 1000.
- **Active vs. started:** `d.state` is 1 for started. `d.is_active` is 1 when
  the torrent is started *and* open. A started torrent that isn't active is
  usually waiting or has an error.
- **`d.message`:** empty when all is well. Otherwise it holds text such as
  `Tracker: [v6 : Could not connect to server | v4 : Failure reason "Torrent is
  not authorized for use on this tracker."]`. Show it in full (F2).
- **`d.custom=addtime`:** the added date, set by `rtorrent.rc` when a torrent
  is added. Torrents migrated from qBittorrent all carry the migration date.
- **`t.url`:** contains the passkey on private trackers. Reduce it to scheme
  and host before it leaves the rTorrent client code (S5).
- **`d.hashing`:** non-zero while a hash check is queued or running;
  `d.is_hash_checking` only while it runs. `d.chunks_hashed` over
  `d.size_chunks` is the check's progress.
- **`d.peers_connected`, `d.peers_complete`, `d.peers_accounted`:** connected
  peers, of which seeds and leechers. They make the status text ("no seeds
  connected", "stalled").
- **`d.directory`:** has no trailing slash, even when `directory.default` had
  one. For a multi-file torrent it is the torrent's own folder.

## Actions

| Purpose | Call |
| --- | --- |
| Add and start | `load.raw_start_verbose "" <base64 .torrent> 'd.directory.set="<dir>"' 'd.custom1.set="<label>"'` |
| Add stopped | `load.raw_verbose` with the same arguments |
| Add a magnet link | `load.start_verbose "" "<magnet uri>" …` (or `load.verbose`) |
| Start / stop | `d.start <hash>` / `d.stop <hash>` |
| Recheck | `d.check_hash <hash>` |
| Reannounce | `d.tracker_announce <hash>` (not `d.tracker_announce.force`, which skips the tracker's minimum interval) |
| Global limits | `throttle.global_down.max_rate.set_kb "" <KiB/s>`, the same for `global_up`; 0 is unlimited |
| Set label | `d.custom1.set <hash> <label>` |
| Remove, keep data | `d.erase <hash>` |
| Remove with data | `d.erase_with_data <hash>`, defined in `rtorrent.rc` (below) |

Rules for these calls:

- The load commands take commands as *strings*. Quote every value
  rTorrent-style: wrap it in double quotes and escape `\` and `"`. A path or
  label containing a comma or a quote would otherwise split or end the
  command. Build these strings in one function and test it (S1).
- Leave `d.directory.set` out to use rTorrent's default (`/downloads/incomplete/`).
  For multi-file torrents, `d.directory.set` names the parent folder; rTorrent
  adds the torrent's name.
- The 0.16 command set has no `load.normal_verbose`. Use `load.verbose` and
  `load.raw_verbose` for stopped loads.
- `load.raw_verbose` returns 0 for data that is not a torrent at all; nothing
  is loaded and no fault is raised. peerglass parses the file itself first
  (name and info-hash) and, after loading, checks that the hash appeared.

Limits are read in bytes per second and set in KiB/s with `.set_kb`.
rTorrent refuses rates outside 0–4,294,967,294 bytes per second ("Throttle
rate must be between 0 and 4294967294."), so the form stops at 4,194,303
KiB/s. A limit set over RPC lasts until rTorrent restarts.

## Quirks seen on 0.16.27

- **`d.erase` deletes the torrent's tied `.torrent` file.** Torrents loaded
  with `load.raw_*` have no tied file, so this doesn't affect them.
- **Erased torrents can't come straight back.** An erased info-hash can't be
  added again while the same rTorrent process runs: the load succeeds but
  nothing appears. Tests that add, erase and re-add must use a fresh hash
  each time (for example, a throwaway torrent with a random name).
- **Error text arrives late.** Right after a restart, rTorrent announces
  every torrent at once. Some private trackers then answer "not authorized"
  for a handful, which clears on later announces. The UI should show this
  like any other message, without treating it as permanent.
- **No useful log file.** rTorrent writes no usable log here; errors reach
  peerglass as XML-RPC faults. Surface them in the UI and in peerglass's
  log.

## Methods defined in rtorrent.rc

These are callable and safe to read, but peerglass must not call the ones that
move data:

- `d.finished_dir <hash>`: the folder a torrent's label maps to. Only torrents
  in `/downloads/incomplete/` move there when they finish; the General tab
  shows it as "Label folder" with that caveat.
- `d.erase_with_data <hash>`: the one data-moving method peerglass may call
  (F10). It stops and closes the torrent, runs `rm -rf` on `d.data_path`,
  then `d.erase`. It refuses unless the path is at least two levels below
  `/downloads` (`/downloads/<folder>/<item>`), so a torrent whose files sit
  directly in a category folder can never take the folder with it. The
  refusal arrives as the fault "Bad return code", rTorrent's wording for a
  failed `execute.throw`.
- `vpn.port`: the forwarded port rTorrent currently follows (global, read).
- `d.sort_finished`, `d.move_to_complete`, `d.keep_on_share`,
  `d.no_parent_dirs`, `d.erase_data_then_torrent`: event handlers and
  helpers. Never call these from peerglass.
