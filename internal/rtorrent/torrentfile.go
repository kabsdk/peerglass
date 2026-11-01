package rtorrent

import (
	"bytes"
	"crypto/sha1"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// TorrentFile is what peerglass needs to know about an uploaded .torrent
// before handing it to rTorrent (F6): that it is one, what it is called and
// which info-hash to expect afterwards.
type TorrentFile struct {
	Name string
	// Hash is the info-hash in upper-case hexadecimal, as rTorrent reports it.
	Hash string
}

// ParseTorrentFile checks that data is a bencoded torrent and returns its
// name and info-hash. load.raw_verbose accepts anything and silently loads
// nothing for junk, so this is how the UI tells the user.
func ParseTorrentFile(data []byte) (TorrentFile, error) {
	p := &bencodeParser{data: data}
	v, err := p.value(0)
	if err != nil {
		return TorrentFile{}, fmt.Errorf("not a torrent file: %w", err)
	}
	top, ok := v.(map[string]any)
	if !ok {
		return TorrentFile{}, errors.New("not a torrent file: no dictionary")
	}
	info, ok := top["info"].(map[string]any)
	if !ok {
		return TorrentFile{}, errors.New("not a torrent file: no info dictionary")
	}
	if p.infoStart < 0 || p.infoEnd <= p.infoStart {
		return TorrentFile{}, errors.New("not a torrent file: info dictionary not located")
	}
	name, _ := info["name"].(string)
	if name == "" {
		return TorrentFile{}, errors.New("not a torrent file: info has no name")
	}
	if _, ok := info["pieces"].(string); !ok {
		return TorrentFile{}, errors.New("not a torrent file: info has no pieces")
	}
	sum := sha1.Sum(data[p.infoStart:p.infoEnd])
	return TorrentFile{Name: name, Hash: strings.ToUpper(hex.EncodeToString(sum[:]))}, nil
}

// bencodeParser decodes bencode into string, int64, []any and
// map[string]any, remembering where the top-level "info" value sits so its
// bytes can be hashed exactly as written.
type bencodeParser struct {
	data               []byte
	pos                int
	infoStart, infoEnd int
}

const maxBencodeDepth = 32

func (p *bencodeParser) value(depth int) (any, error) {
	if depth > maxBencodeDepth {
		return nil, errors.New("nested too deeply")
	}
	if p.pos >= len(p.data) {
		return nil, errors.New("unexpected end")
	}
	switch c := p.data[p.pos]; {
	case c == 'i':
		end := bytes.IndexByte(p.data[p.pos:], 'e')
		if end < 0 {
			return nil, errors.New("unterminated integer")
		}
		n, err := strconv.ParseInt(string(p.data[p.pos+1:p.pos+end]), 10, 64)
		if err != nil {
			return nil, errors.New("bad integer")
		}
		p.pos += end + 1
		return n, nil
	case c == 'l':
		p.pos++
		list := []any{}
		for {
			if p.pos >= len(p.data) {
				return nil, errors.New("unterminated list")
			}
			if p.data[p.pos] == 'e' {
				p.pos++
				return list, nil
			}
			v, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
	case c == 'd':
		p.pos++
		dict := map[string]any{}
		for {
			if p.pos >= len(p.data) {
				return nil, errors.New("unterminated dictionary")
			}
			if p.data[p.pos] == 'e' {
				p.pos++
				return dict, nil
			}
			key, err := p.str()
			if err != nil {
				return nil, err
			}
			start := p.pos
			v, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			if depth == 0 && key == "info" {
				p.infoStart, p.infoEnd = start, p.pos
			}
			dict[key] = v
		}
	case c >= '0' && c <= '9':
		return p.str()
	}
	return nil, fmt.Errorf("unexpected byte %q", p.data[p.pos])
}

func (p *bencodeParser) str() (string, error) {
	colon := bytes.IndexByte(p.data[p.pos:], ':')
	if colon < 0 {
		return "", errors.New("unterminated string length")
	}
	n, err := strconv.Atoi(string(p.data[p.pos : p.pos+colon]))
	if err != nil || n < 0 {
		return "", errors.New("bad string length")
	}
	start := p.pos + colon + 1
	if start+n > len(p.data) {
		return "", errors.New("string past end")
	}
	p.pos = start + n
	return string(p.data[start:p.pos]), nil
}

// MagnetHash validates a magnet link (S8: the magnet: scheme only) and
// returns its info-hash in upper-case hexadecimal.
func MagnetHash(uri string) (string, error) {
	uri = strings.TrimSpace(uri)
	u, err := url.Parse(uri)
	if err != nil || !strings.EqualFold(u.Scheme, "magnet") {
		return "", errors.New("not a magnet link")
	}
	for _, xt := range u.Query()["xt"] {
		rest, ok := cutPrefixFold(xt, "urn:btih:")
		if !ok {
			continue
		}
		switch len(rest) {
		case 40:
			if ValidHash(rest) {
				return strings.ToUpper(rest), nil
			}
		case 32:
			raw, err := base32.StdEncoding.DecodeString(strings.ToUpper(rest))
			if err == nil && len(raw) == 20 {
				return strings.ToUpper(hex.EncodeToString(raw)), nil
			}
		}
	}
	return "", errors.New("magnet link has no valid btih info-hash")
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return "", false
}
