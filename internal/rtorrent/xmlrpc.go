package rtorrent

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Fault is an error rTorrent reported as an XML-RPC fault: an unknown
// method, a bad hash, an invalid torrent file, and so on.
type Fault struct {
	Code    int64
	Message string
}

func (f *Fault) Error() string {
	return fmt.Sprintf("rtorrent: %s (fault %d)", f.Message, f.Code)
}

// member is one entry of an orderedStruct.
type member struct {
	Name  string
	Value any
}

// orderedStruct is an XML-RPC struct whose member order is kept. rTorrent
// insists that a system.multicall entry names methodName before params.
type orderedStruct []member

// encodeCall writes an XML-RPC methodCall. Parameters may be string, int,
// int64, []byte (sent as base64), []any (array), orderedStruct or
// map[string]any (struct, members in key order).
func encodeCall(method string, args []any) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0"?><methodCall><methodName>`)
	if err := xml.EscapeText(&b, []byte(method)); err != nil {
		return nil, err
	}
	b.WriteString(`</methodName><params>`)
	for _, a := range args {
		b.WriteString(`<param>`)
		if err := encodeValue(&b, a); err != nil {
			return nil, err
		}
		b.WriteString(`</param>`)
	}
	b.WriteString(`</params></methodCall>`)
	return b.Bytes(), nil
}

func encodeValue(b *bytes.Buffer, v any) error {
	b.WriteString(`<value>`)
	switch v := v.(type) {
	case string:
		b.WriteString(`<string>`)
		if err := xml.EscapeText(b, []byte(v)); err != nil {
			return err
		}
		b.WriteString(`</string>`)
	case int:
		fmt.Fprintf(b, `<i8>%d</i8>`, v)
	case int64:
		fmt.Fprintf(b, `<i8>%d</i8>`, v)
	case bool:
		if v {
			b.WriteString(`<boolean>1</boolean>`)
		} else {
			b.WriteString(`<boolean>0</boolean>`)
		}
	case float64:
		fmt.Fprintf(b, `<double>%g</double>`, v)
	case nil:
		b.WriteString(`<nil/>`)
	case []byte:
		b.WriteString(`<base64>`)
		b.WriteString(base64.StdEncoding.EncodeToString(v))
		b.WriteString(`</base64>`)
	case []any:
		b.WriteString(`<array><data>`)
		for _, e := range v {
			if err := encodeValue(b, e); err != nil {
				return err
			}
		}
		b.WriteString(`</data></array>`)
	case orderedStruct:
		b.WriteString(`<struct>`)
		for _, m := range v {
			if err := encodeMember(b, m.Name, m.Value); err != nil {
				return err
			}
		}
		b.WriteString(`</struct>`)
	case map[string]any:
		b.WriteString(`<struct>`)
		for _, name := range slices.Sorted(maps.Keys(v)) {
			if err := encodeMember(b, name, v[name]); err != nil {
				return err
			}
		}
		b.WriteString(`</struct>`)
	default:
		return fmt.Errorf("unsupported XML-RPC parameter type %T", v)
	}
	b.WriteString(`</value>`)
	return nil
}

func encodeMember(b *bytes.Buffer, name string, v any) error {
	b.WriteString(`<member><name>`)
	if err := xml.EscapeText(b, []byte(name)); err != nil {
		return err
	}
	b.WriteString(`</name>`)
	if err := encodeValue(b, v); err != nil {
		return err
	}
	b.WriteString(`</member>`)
	return nil
}

// decodeResponse parses a methodResponse. The result is a string, int64,
// bool, float64, []byte, []any, map[string]any or nil. A fault response is
// returned as a *Fault error.
func decodeResponse(data []byte) (any, error) {
	d := &decoder{dec: xml.NewDecoder(bytes.NewReader(sanitizeXML(data)))}
	if _, err := d.start("methodResponse"); err != nil {
		return nil, err
	}
	se, err := d.start("")
	if err != nil {
		return nil, err
	}
	switch se.Name.Local {
	case "params":
		if _, err := d.start("param"); err != nil {
			return nil, err
		}
		if _, err := d.start("value"); err != nil {
			return nil, err
		}
		return d.value()
	case "fault":
		if _, err := d.start("value"); err != nil {
			return nil, err
		}
		v, err := d.value()
		if err != nil {
			return nil, err
		}
		m, _ := v.(map[string]any)
		code, _ := m["faultCode"].(int64)
		msg, _ := m["faultString"].(string)
		if msg == "" {
			msg = "unknown fault"
		}
		return nil, &Fault{Code: code, Message: msg}
	default:
		return nil, fmt.Errorf("unexpected element <%s> in methodResponse", se.Name.Local)
	}
}

// sanitizeXML replaces invalid UTF-8 and control characters, which can
// reach us raw through torrent names, so one odd torrent cannot break the
// whole response.
func sanitizeXML(data []byte) []byte {
	clean := true
	for i := 0; i < len(data); {
		r, size := utf8.DecodeRune(data[i:])
		if (r == utf8.RuneError && size == 1) || (r < 0x20 && r != '\t' && r != '\n' && r != '\r') {
			clean = false
			break
		}
		i += size
	}
	if clean {
		return data
	}
	var b bytes.Buffer
	b.Grow(len(data))
	for i := 0; i < len(data); {
		r, size := utf8.DecodeRune(data[i:])
		if (r == utf8.RuneError && size == 1) || (r < 0x20 && r != '\t' && r != '\n' && r != '\r') {
			b.WriteRune(utf8.RuneError)
		} else {
			b.Write(data[i : i+size])
		}
		i += size
	}
	return b.Bytes()
}

type decoder struct {
	dec *xml.Decoder
}

// next returns the next token that is not whitespace, a comment or a
// processing instruction.
func (d *decoder) next() (xml.Token, error) {
	for {
		tok, err := d.dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, errors.New("truncated XML-RPC response")
			}
			return nil, err
		}
		switch t := tok.(type) {
		case xml.CharData:
			if len(bytes.TrimSpace(t)) == 0 {
				continue
			}
			return t, nil
		case xml.StartElement, xml.EndElement:
			return t, nil
		}
	}
}

// start reads the next start element. With a non-empty name it must be that
// element.
func (d *decoder) start(name string) (xml.StartElement, error) {
	tok, err := d.next()
	if err != nil {
		return xml.StartElement{}, err
	}
	se, ok := tok.(xml.StartElement)
	if !ok {
		return xml.StartElement{}, fmt.Errorf("expected <%s>, got %T", name, tok)
	}
	if name != "" && se.Name.Local != name {
		return xml.StartElement{}, fmt.Errorf("expected <%s>, got <%s>", name, se.Name.Local)
	}
	return se, nil
}

// end reads the next token and requires it to be an end element.
func (d *decoder) end() error {
	tok, err := d.next()
	if err != nil {
		return err
	}
	if _, ok := tok.(xml.EndElement); !ok {
		return fmt.Errorf("expected end element, got %T", tok)
	}
	return nil
}

// text reads character data up to and including the current element's end
// tag.
func (d *decoder) text() (string, error) {
	var b strings.Builder
	for {
		tok, err := d.dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return "", errors.New("truncated XML-RPC response")
			}
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			b.Write(t)
		case xml.EndElement:
			return b.String(), nil
		case xml.StartElement:
			return "", fmt.Errorf("unexpected <%s> inside scalar value", t.Name.Local)
		}
	}
}

// value decodes the contents of a <value> element, consuming its end tag.
func (d *decoder) value() (any, error) {
	var text strings.Builder
	for {
		tok, err := d.dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, errors.New("truncated XML-RPC response")
			}
			return nil, err
		}
		switch t := tok.(type) {
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			// An untyped <value>text</value> is a string.
			return text.String(), nil
		case xml.StartElement:
			v, err := d.typed(t.Name.Local)
			if err != nil {
				return nil, err
			}
			if err := d.end(); err != nil {
				return nil, err
			}
			return v, nil
		}
	}
}

// typed decodes a typed value element whose start tag has been read,
// consuming its end tag.
func (d *decoder) typed(name string) (any, error) {
	switch name {
	case "string":
		return d.text()
	case "i4", "i8", "int":
		s, err := d.text()
		if err != nil {
			return nil, err
		}
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bad integer %q", s)
		}
		return n, nil
	case "boolean":
		s, err := d.text()
		if err != nil {
			return nil, err
		}
		switch strings.TrimSpace(s) {
		case "1", "true":
			return true, nil
		case "0", "false":
			return false, nil
		}
		return nil, fmt.Errorf("bad boolean %q", s)
	case "double":
		s, err := d.text()
		if err != nil {
			return nil, err
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return nil, fmt.Errorf("bad double %q", s)
		}
		return f, nil
	case "base64":
		s, err := d.text()
		if err != nil {
			return nil, err
		}
		return base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
	case "nil":
		return nil, d.end()
	case "array":
		if _, err := d.start("data"); err != nil {
			return nil, err
		}
		items := []any{}
		for {
			tok, err := d.next()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.EndElement: // </data>
				return items, d.end() // </array>
			case xml.StartElement:
				if t.Name.Local != "value" {
					return nil, fmt.Errorf("unexpected <%s> in array", t.Name.Local)
				}
				v, err := d.value()
				if err != nil {
					return nil, err
				}
				items = append(items, v)
			default:
				return nil, fmt.Errorf("unexpected text in array")
			}
		}
	case "struct":
		m := map[string]any{}
		for {
			tok, err := d.next()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.EndElement: // </struct>
				return m, nil
			case xml.StartElement:
				if t.Name.Local != "member" {
					return nil, fmt.Errorf("unexpected <%s> in struct", t.Name.Local)
				}
				if _, err := d.start("name"); err != nil {
					return nil, err
				}
				key, err := d.text()
				if err != nil {
					return nil, err
				}
				if _, err := d.start("value"); err != nil {
					return nil, err
				}
				v, err := d.value()
				if err != nil {
					return nil, err
				}
				if err := d.end(); err != nil { // </member>
					return nil, err
				}
				m[strings.TrimSpace(key)] = v
			default:
				return nil, fmt.Errorf("unexpected text in struct")
			}
		}
	}
	return nil, fmt.Errorf("unknown XML-RPC type <%s>", name)
}

// DecodeCall parses an XML-RPC methodCall into its method name and
// parameters. It exists for fakes that stand in for rTorrent in tests.
func DecodeCall(data []byte) (method string, params []any, err error) {
	d := &decoder{dec: xml.NewDecoder(bytes.NewReader(sanitizeXML(data)))}
	if _, err := d.start("methodCall"); err != nil {
		return "", nil, err
	}
	if _, err := d.start("methodName"); err != nil {
		return "", nil, err
	}
	method, err = d.text()
	if err != nil {
		return "", nil, err
	}
	method = strings.TrimSpace(method)
	params = []any{}
	tok, err := d.next()
	if err != nil {
		return "", nil, err
	}
	se, ok := tok.(xml.StartElement)
	if !ok || se.Name.Local != "params" {
		return method, params, nil
	}
	for {
		tok, err := d.next()
		if err != nil {
			return "", nil, err
		}
		switch t := tok.(type) {
		case xml.EndElement: // </params>
			return method, params, nil
		case xml.StartElement:
			if t.Name.Local != "param" {
				return "", nil, fmt.Errorf("unexpected <%s> in params", t.Name.Local)
			}
			if _, err := d.start("value"); err != nil {
				return "", nil, err
			}
			v, err := d.value()
			if err != nil {
				return "", nil, err
			}
			if err := d.end(); err != nil { // </param>
				return "", nil, err
			}
			params = append(params, v)
		default:
			return "", nil, errors.New("unexpected text in params")
		}
	}
}

// EncodeResponse writes a methodResponse carrying v. It exists for fakes.
func EncodeResponse(v any) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0"?><methodResponse><params><param>`)
	if err := encodeValue(&b, v); err != nil {
		return nil, err
	}
	b.WriteString(`</param></params></methodResponse>`)
	return b.Bytes(), nil
}

// EncodeFault writes a fault methodResponse. It exists for fakes.
func EncodeFault(code int64, message string) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0"?><methodResponse><fault>`)
	// Two fixed keys and scalar values: encodeValue cannot fail here.
	_ = encodeValue(&b, map[string]any{"faultCode": code, "faultString": message})
	b.WriteString(`</fault></methodResponse>`)
	return b.Bytes()
}
