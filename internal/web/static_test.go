package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// The only third-party code shipped to browsers is htmx, pinned by the
// hash recorded next to it (S7). A changed file fails here before it ships.
func TestVendoredHtmxMatchesRecordedHash(t *testing.T) {
	js, err := fs.ReadFile(staticFS, "static/htmx.min.js")
	if err != nil {
		t.Fatal(err)
	}
	record, err := fs.ReadFile(staticFS, "static/htmx.min.js.sha256")
	if err != nil {
		t.Fatal(err)
	}
	var want string
	for _, line := range strings.Split(string(record), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == "htmx.min.js" {
			want = f[0]
		}
	}
	if want == "" {
		t.Fatal("no hash line for htmx.min.js in htmx.min.js.sha256")
	}
	sum := sha256.Sum256(js)
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("htmx.min.js has SHA-256 %s, the record says %s", got, want)
	}
}

// Our own scripts may only toggle, position and ask htmx to fetch. They must
// never build markup from data or evaluate strings (S2).
func TestOwnScriptsBuildNoMarkup(t *testing.T) {
	forbidden := regexp.MustCompile(`innerHTML|outerHTML|insertAdjacentHTML|\beval\s*\(|new\s+Function|document\.write|javascript:|setAttribute\(\s*['"]on|srcdoc|createContextualFragment|DOMParser`)
	entries, err := fs.ReadDir(staticFS, "static")
	if err != nil {
		t.Fatal(err)
	}
	var own int
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".js") || e.Name() == "htmx.min.js" {
			continue
		}
		own++
		src, err := fs.ReadFile(staticFS, "static/"+e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if m := forbidden.FindString(line); m != "" {
				t.Errorf("%s:%d uses %q, which our scripts may not", e.Name(), i+1, m)
			}
		}
	}
	if own == 0 {
		t.Skip("no scripts of our own")
	}
}
