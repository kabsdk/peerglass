package rtorrent

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestScgiRequest(t *testing.T) {
	got := string(scgiRequest([]byte("abc")))
	want := "24:CONTENT_LENGTH\x003\x00SCGI\x001\x00,abc"
	if got != want {
		t.Fatalf("scgiRequest = %q, want %q", got, want)
	}
}

func TestSplitSCGIResponse(t *testing.T) {
	body, err := splitSCGIResponse([]byte("Status: 200 OK\r\nContent-Type: text/xml\r\n\r\n<x/>"))
	if err != nil || string(body) != "<x/>" {
		t.Fatalf("got %q, %v", body, err)
	}
	if _, err := splitSCGIResponse([]byte("no headers here")); err == nil {
		t.Fatal("expected an error without a header terminator")
	}
}

func TestEncodeCall(t *testing.T) {
	got, err := encodeCall("load.raw_start_verbose", []any{"", []byte{0, 1, 2}, int64(7), 8, []any{"a"}, map[string]any{"k": "v"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `<?xml version="1.0"?><methodCall><methodName>load.raw_start_verbose</methodName><params>` +
		`<param><value><string></string></value></param>` +
		`<param><value><base64>AAEC</base64></value></param>` +
		`<param><value><i8>7</i8></value></param>` +
		`<param><value><i8>8</i8></value></param>` +
		`<param><value><array><data><value><string>a</string></value></data></array></value></param>` +
		`<param><value><struct><member><name>k</name><value><string>v</string></value></member></struct></value></param>` +
		`</params></methodCall>`
	if string(got) != want {
		t.Fatalf("encodeCall =\n%s\nwant\n%s", got, want)
	}
	if _, err := encodeCall("x", []any{struct{}{}}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported type: got %v", err)
	}
}

// rTorrent requires methodName before params in each system.multicall
// entry; a Go map would put them in random order.
func TestMulticallMemberOrder(t *testing.T) {
	for i := 0; i < 20; i++ {
		got, err := encodeCall("system.multicall", []any{[]any{orderedStruct{{"methodName", "d.name"}, {"params", []any{"H"}}}}})
		if err != nil {
			t.Fatal(err)
		}
		m, p := strings.Index(string(got), "<name>methodName</name>"), strings.Index(string(got), "<name>params</name>")
		if m < 0 || p < 0 || m > p {
			t.Fatalf("members out of order: %s", got)
		}
	}
	got, _ := encodeCall("x", []any{map[string]any{"b": int64(1), "a": int64(2)}})
	if !strings.Contains(string(got), `<name>a</name><value><i8>2</i8></value></member><member><name>b</name>`) {
		t.Fatalf("map members should be in key order: %s", got)
	}
}

func TestEncodeEscapes(t *testing.T) {
	got, err := encodeCall("d.custom1.set", []any{"HASH", `a<b>&"c"`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `<string>a&lt;b&gt;&amp;&#34;c&#34;</string>`) {
		t.Fatalf("string not escaped: %s", got)
	}
}

func TestDecodeResponse(t *testing.T) {
	wrap := func(v string) []byte {
		return []byte(`<?xml version="1.0"?><methodResponse><params><param>` + v + `</param></params></methodResponse>`)
	}
	tests := []struct {
		name string
		in   string
		want any
	}{
		{"string", `<value><string>0.16.27</string></value>`, "0.16.27"},
		{"untyped", `<value>plain</value>`, "plain"},
		{"empty untyped", `<value></value>`, ""},
		{"i8", `<value><i8>-500</i8></value>`, int64(-500)},
		{"i4", `<value><i4>42</i4></value>`, int64(42)},
		{"int", `<value><int>42</int></value>`, int64(42)},
		{"boolean", `<value><boolean>1</boolean></value>`, true},
		{"double", `<value><double>1.5</double></value>`, 1.5},
		{"base64", `<value><base64>AAEC</base64></value>`, []byte{0, 1, 2}},
		{"nil", `<value><nil/></value>`, nil},
		{"nested array", `<value><array><data>
			<value><array><data><value><string>a</string></value><value><i8>1</i8></value></data></array></value>
			<value><array><data></data></array></value>
		</data></array></value>`, []any{[]any{"a", int64(1)}, []any{}}},
		{"struct", `<value><struct><member><name>k</name><value><i8>1</i8></value></member></struct></value>`, map[string]any{"k": int64(1)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeResponse(wrap(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestDecodeFault(t *testing.T) {
	data, err := os.ReadFile("testdata/fault.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, err = decodeResponse(data)
	var f *Fault
	if !errors.As(err, &f) {
		t.Fatalf("expected *Fault, got %v", err)
	}
	if f.Code != -500 || f.Message != "invalid parameters: invalid target" {
		t.Fatalf("fault = %+v", f)
	}
	if !strings.Contains(f.Error(), "invalid target") {
		t.Fatalf("Error() = %q", f.Error())
	}
}

func TestDecodeBadInput(t *testing.T) {
	for _, in := range []string{
		"",
		`<?xml version="1.0"?><methodResponse><params><param><value><string>trunc`,
		`<?xml version="1.0"?><methodResponse><params><param><value><wat>1</wat></value></param></params></methodResponse>`,
		`<?xml version="1.0"?><methodResponse><params><param><value><i8>x</i8></value></param></params></methodResponse>`,
		`not xml at all`,
	} {
		if _, err := decodeResponse([]byte(in)); err == nil {
			t.Errorf("no error for %q", in)
		}
	}
}

func TestDecodeSanitizesInvalidUTF8(t *testing.T) {
	in := []byte(`<?xml version="1.0"?><methodResponse><params><param><value><string>a` + "\xff\x01" + `b</string></value></param></params></methodResponse>`)
	got, err := decodeResponse(in)
	if err != nil {
		t.Fatal(err)
	}
	if got != "a��b" {
		t.Fatalf("got %q", got)
	}
}

func TestDecodeCallRoundTrip(t *testing.T) {
	body, err := encodeCall("d.multicall2", []any{"", "main", "d.hash=", []byte("raw"), int64(3), map[string]any{"methodName": "d.name", "params": []any{"H"}}})
	if err != nil {
		t.Fatal(err)
	}
	method, params, err := DecodeCall(body)
	if err != nil {
		t.Fatal(err)
	}
	want := []any{"", "main", "d.hash=", []byte("raw"), int64(3), map[string]any{"methodName": "d.name", "params": []any{"H"}}}
	if method != "d.multicall2" || !reflect.DeepEqual(params, want) {
		t.Fatalf("got %s %#v", method, params)
	}
	resp, err := EncodeResponse([]any{"x", int64(1)})
	if err != nil {
		t.Fatal(err)
	}
	v, err := decodeResponse(resp)
	if err != nil || !reflect.DeepEqual(v, []any{"x", int64(1)}) {
		t.Fatalf("response round trip: %#v %v", v, err)
	}
	_, err = decodeResponse(EncodeFault(-506, "Method 'x' not defined"))
	var f *Fault
	if !errors.As(err, &f) || f.Code != -506 {
		t.Fatalf("fault round trip: %v", err)
	}
}
