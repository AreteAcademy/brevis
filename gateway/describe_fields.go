package gateway

import (
	"reflect"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ManifestField is one setting of a stream, by the path the file spells it
// with: `buffer.flush.every`, `sink.into.write`.
type ManifestField struct {
	Path string `json:"path"`

	// Value is a string, a number, a bool or a list of them. Durations and
	// sizes are written the way the file writes them -- `1m`, `256MiB` --
	// and not as the integers Go holds them in.
	Value any `json:"value"`

	// Default marks a value the file did not write and the gateway filled
	// in. Never set when there is no file to compare with.
	Default bool `json:"default,omitempty"`
}

// describeFields is a stream as it runs, one leaf per setting.
//
// IT WALKS THE YAML TAGS, so a field added to Stream is described with no
// change here. That makes this file safe only while the config holds no
// secret, and the convention that keeps it so -- a variable's NAME under a
// `_from` key, never its value -- is enforced by TestNoStreamFieldLooksLikeASecret.
//
// A zero value is left out unless the file wrote it: `topic: ""` on a postgres
// stream is noise, `stamp_loaded_at: false` written by somebody is a decision.
func describeFields(st Stream, node *yaml.Node) []ManifestField {
	written := map[string]bool{}
	collectKeys(node, "", written)
	var out []ManifestField
	walkFields(reflect.ValueOf(st), "", written, node != nil, &out)
	return out
}

var (
	durationType    = reflect.TypeOf(time.Duration(0))
	ownDurationType = reflect.TypeOf(Duration(0))
	sizeType        = reflect.TypeOf(Size(0))
)

func walkFields(v reflect.Value, prefix string, written map[string]bool, haveFile bool, out *[]ManifestField) {
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if !f.IsExported() || name == "" || name == "-" {
			continue
		}
		path := prefix + name
		fv := v.Field(i)
		if fv.Kind() == reflect.Pointer {
			if fv.IsNil() {
				continue
			}
			fv = fv.Elem()
		}
		if fv.Kind() == reflect.Struct {
			walkFields(fv, path+".", written, haveFile, out)
			continue
		}
		if fv.IsZero() && !written[path] {
			continue
		}
		*out = append(*out, ManifestField{
			Path:    path,
			Value:   leafValue(fv),
			Default: haveFile && !written[path],
		})
	}
}

// leafValue is a setting as JSON carries it, and as somebody reading the
// file would recognise it.
func leafValue(v reflect.Value) any {
	switch v.Type() {
	case durationType, ownDurationType:
		return durationText(time.Duration(v.Int()))
	case sizeType:
		return Size(v.Int()).String()
	}
	switch v.Kind() {
	case reflect.String:
		return v.String()
	case reflect.Bool:
		return v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int(v.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int(v.Uint())
	case reflect.Float32, reflect.Float64:
		return v.Float()
	case reflect.Slice:
		list := make([]any, v.Len())
		for i := range list {
			list[i] = leafValue(v.Index(i))
		}
		return list
	}
	return v.Interface()
}

// durationText is Go's spelling without the trailing zeroes: `1m`, not
// `1m0s`, which is how the file wrote it and how anybody would.
func durationText(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// streamNode is the i-th entry under `streams:`, or nil without a file.
func streamNode(doc *yaml.Node, i int) *yaml.Node {
	if doc == nil || len(doc.Content) == 0 {
		return nil
	}
	streams := mappingValue(doc.Content[0], "streams")
	if streams == nil || streams.Kind != yaml.SequenceNode || i >= len(streams.Content) {
		return nil
	}
	return streams.Content[i]
}

func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for j := 0; j+1 < len(n.Content); j += 2 {
		if n.Content[j].Value == key {
			return n.Content[j+1]
		}
	}
	return nil
}

// collectKeys records every path the file wrote, sections included.
func collectKeys(n *yaml.Node, prefix string, out map[string]bool) {
	if n == nil || n.Kind != yaml.MappingNode {
		return
	}
	for j := 0; j+1 < len(n.Content); j += 2 {
		path := prefix + n.Content[j].Value
		out[path] = true
		collectKeys(n.Content[j+1], path+".", out)
	}
}

// describeNotes is every comment of a stream, by the path of the setting it
// sits on: the lines above a key, and the one at the end of its line.
//
// The comment above `- name:` is about the stream as a whole, and YAML hangs
// it on the stream's own node rather than on a key. It is kept under the
// empty path.
func describeNotes(n *yaml.Node) map[string]string {
	notes := map[string]string{}
	if n != nil {
		if t := commentText(n.HeadComment); t != "" {
			notes[""] = t
		}
	}
	collectNotes(n, "", notes)
	if len(notes) == 0 {
		return nil
	}
	return notes
}

func collectNotes(n *yaml.Node, prefix string, out map[string]string) {
	if n == nil || n.Kind != yaml.MappingNode {
		return
	}
	for j := 0; j+1 < len(n.Content); j += 2 {
		key, val := n.Content[j], n.Content[j+1]
		path := prefix + key.Value
		var parts []string
		for _, c := range []string{key.HeadComment, key.LineComment, val.LineComment} {
			if t := commentText(c); t != "" {
				parts = append(parts, t)
			}
		}
		if len(parts) > 0 {
			out[path] = strings.Join(parts, "\n\n")
		}
		collectNotes(val, path+".", out)
	}
}

// noteMarker is how an author says a comment is meant for /data: `#:`.
//
// OPT-IN, and that is the whole guarantee. A comment is free text that no
// `_from` convention reaches, and a secret ends up in one in ways no pattern
// can recognise -- a commented-out setting, a password on the line after
// "the password is". An ordinary `#` comment therefore never leaves the
// gateway; only the lines its author marked do. The engine still checks what
// they say (catalog.FindSecretInText), as the second wall.
const noteMarker = "#:"

// commentText is the marked lines of a comment, without the marker and the
// one space after it, as their author laid them out. A bare `#:` is a blank
// line between paragraphs. Everything else is dropped.
func commentText(c string) string {
	if c == "" {
		return ""
	}
	var kept []string
	for _, l := range strings.Split(c, "\n") {
		l = strings.TrimSpace(l)
		text, marked := strings.CutPrefix(l, noteMarker)
		if !marked {
			continue
		}
		kept = append(kept, strings.TrimPrefix(text, " "))
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}
