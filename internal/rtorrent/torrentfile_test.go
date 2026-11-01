package rtorrent

import (
	"crypto/sha1"
	"encoding/base32"
	"encoding/hex"
	"strings"
	"testing"
)

func TestParseTorrentFile(t *testing.T) {
	info := "d6:lengthi3e4:name8:test.iso12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaae"
	data := []byte("d8:announce31:http://tracker.example.invalid/4:info" + info + "e")
	tf, err := ParseTorrentFile(data)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha1.Sum([]byte(info))
	if tf.Name != "test.iso" || tf.Hash != strings.ToUpper(hex.EncodeToString(sum[:])) {
		t.Fatalf("got %+v", tf)
	}
	for _, bad := range []string{
		"", "junk", "d4:infoi1ee", "d4:infod4:name1:xee", "de", "l4:infoe",
		"d4:infod4:name0:6:pieces0:ee",
		"d4:infod4:name1:x6:pieces20:aaaaaaaaaaaaaaaaaaaae", // unterminated outer dict
		"d4:info" + info + "e",                              // valid: control for the loop below
	} {
		_, err := ParseTorrentFile([]byte(bad))
		if strings.HasPrefix(bad, "d4:info"+info) {
			if err != nil {
				t.Errorf("minimal torrent rejected: %v", err)
			}
			continue
		}
		if err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	deep := strings.Repeat("l", 40) + strings.Repeat("e", 40)
	if _, err := ParseTorrentFile([]byte(deep)); err == nil {
		t.Error("deep nesting accepted")
	}
}

func TestMagnetHash(t *testing.T) {
	hexHash := "7acf8fb590b2060dd9c3146ef770169d593433b0"
	got, err := MagnetHash("magnet:?xt=urn:btih:" + hexHash + "&dn=x&tr=http%3A%2F%2Ft.example.invalid%2Fa")
	if err != nil || got != strings.ToUpper(hexHash) {
		t.Fatalf("hex: %q %v", got, err)
	}
	raw, _ := hex.DecodeString(hexHash)
	got, err = MagnetHash("MAGNET:?xt=urn:btih:" + strings.ToLower(base32.StdEncoding.EncodeToString(raw)))
	if err != nil || got != strings.ToUpper(hexHash) {
		t.Fatalf("base32: %q %v", got, err)
	}
	for _, bad := range []string{"", "http://example.invalid/?xt=urn:btih:" + hexHash, "magnet:?dn=x", "magnet:?xt=urn:btih:short", "javascript:alert(1)"} {
		if _, err := MagnetHash(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
