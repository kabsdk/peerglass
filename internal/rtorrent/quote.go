package rtorrent

import "strings"

// quote wraps v for use as a value inside a command passed to a load.*
// call, rTorrent-style: double quotes around it, with backslashes and
// double quotes escaped. Without this a comma would split the command and a
// quote would end it (S1, docs/rtorrent.md).
func quote(v string) string {
	var b strings.Builder
	b.Grow(len(v) + 2)
	b.WriteByte('"')
	for i := 0; i < len(v); i++ {
		if v[i] == '\\' || v[i] == '"' {
			b.WriteByte('\\')
		}
		b.WriteByte(v[i])
	}
	b.WriteByte('"')
	return b.String()
}

// setCommand builds `<method>=<quoted value>`, the form the load commands
// take for per-torrent settings such as d.directory.set and d.custom1.set.
func setCommand(method, value string) string {
	return method + "=" + quote(value)
}
