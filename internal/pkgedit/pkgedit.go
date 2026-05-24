// Package pkgedit loads and saves package.json while preserving the
// top-level key order (npm reorders dependency objects alphabetically but
// keeps the document's field order, which this mirrors). Used by add /
// remove and the pkg command.
package pkgedit

import (
	"bytes"
	"encoding/json"
	"os"
	"sort"

	"github.com/koji-1009/gnpm/internal/core"
)

// Doc is an editable package.json: the decoded values plus the original
// top-level key order.
type Doc struct {
	Values map[string]any
	Order  []string
	path   string
}

// Load reads and decodes package.json, capturing its top-level key order.
func Load(path string) (*Doc, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, core.Usage("reading %s: %v", path, err)
	}
	var values map[string]any
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, core.Usage("%s is not valid JSON: %v", path, err)
	}
	order, err := topLevelKeyOrder(data)
	if err != nil {
		return nil, core.Usage("scanning %s: %v", path, err)
	}
	return &Doc{Values: values, Order: order, path: path}, nil
}

// Save writes the document back, emitting top-level keys in their
// captured order (new keys appended), with two-space indentation.
func (d *Doc) Save() error {
	body, err := d.Marshal()
	if err != nil {
		return err
	}
	if err := os.WriteFile(d.path, body, 0o644); err != nil {
		return core.IOError("writing %s", d.path).Wrap(err)
	}
	return nil
}

// Marshal renders the document to bytes (no file I/O).
func (d *Doc) Marshal() ([]byte, error) {
	keys := d.orderedKeys()
	var b bytes.Buffer
	b.WriteString("{\n")
	for i, k := range keys {
		kb, _ := json.Marshal(k)
		vb, err := json.MarshalIndent(d.Values[k], "  ", "  ")
		if err != nil {
			return nil, core.IOError("encoding field %s", k).Wrap(err)
		}
		b.WriteString("  ")
		b.Write(kb)
		b.WriteString(": ")
		b.Write(vb)
		if i < len(keys)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString("}\n")
	return b.Bytes(), nil
}

func (d *Doc) orderedKeys() []string {
	seen := map[string]bool{}
	var keys []string
	for _, k := range d.Order {
		if _, ok := d.Values[k]; ok {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	var rest []string
	for k := range d.Values {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	return append(keys, rest...)
}

// DepField returns the dependency object for field (creating it if
// makeIf is set), as a map[string]any.
func (d *Doc) DepField(field string, create bool) map[string]any {
	if m, ok := d.Values[field].(map[string]any); ok {
		return m
	}
	if !create {
		return nil
	}
	m := map[string]any{}
	d.Values[field] = m
	return m
}

// topLevelKeyOrder scans the JSON object and returns its top-level keys
// in document order. Within the root object, tokens alternate key→value;
// nested containers are tracked by depth so only root-level keys are
// recorded.
func topLevelKeyOrder(data []byte) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, core.Usage("package.json root is not an object")
	}
	var order []string
	depth := 0 // depth below the root object's interior
	wantKey := true
	for {
		t, err := dec.Token()
		if err != nil {
			break
		}
		if d, ok := t.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
			case '}', ']':
				if depth == 0 {
					return order, nil // closing the root object
				}
				depth--
				if depth == 0 {
					wantKey = true // a root-level container value just ended
				}
			}
			continue
		}
		if depth != 0 {
			continue
		}
		if wantKey {
			if s, ok := t.(string); ok {
				order = append(order, s)
			}
			wantKey = false // next token is this key's value
		} else {
			wantKey = true // a scalar value just ended; next is a key
		}
	}
	return order, nil
}
