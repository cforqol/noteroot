package frontmatter

import (
	"errors"
	"reflect"
	"testing"
)

const validID = "01ARZ3NDEKTSV4RRFFQ69G5FAV00"

func TestSplit(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantOK    bool
		wantBlock string
		wantBody  string
	}{
		{"no frontmatter", "# hi\n", false, "", "# hi\n"},
		{"basic", "---\na: 1\n---\nbody", true, "a: 1\n", "body"},
		{"crlf", "---\r\na: 1\r\n---\r\nbody", true, "a: 1\r\n", "body"},
		{"empty block", "---\n---\nbody", true, "", "body"},
		{"closing at EOF no newline", "---\na: 1\n---", true, "a: 1\n", ""},
		{"unterminated", "---\na: 1\nbody", false, "", "---\na: 1\nbody"},
		{"opening only", "---", false, "", "---"},
		{"not a delimiter", "---x\na: 1\n---\n", false, "", "---x\na: 1\n---\n"},
		{"empty input", "", false, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block, body, ok := Split([]byte(tt.in))
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if string(block) != tt.wantBlock || string(body) != tt.wantBody {
				t.Errorf("block=%q body=%q, want block=%q body=%q", block, body, tt.wantBlock, tt.wantBody)
			}
		})
	}
}

func TestSplit_oversizedBlockTreatedAsBody(t *testing.T) {
	big := make([]byte, maxBlock+10)
	for i := range big {
		big[i] = 'a'
	}
	src := append([]byte("---\n"), big...)
	src = append(src, []byte("\n---\n")...)
	if _, _, ok := Split(src); ok {
		t.Error("oversized block should not parse as frontmatter")
	}
}

func TestParse_valid(t *testing.T) {
	src := "---\nid: " + validID + "\ntitle: Hello\ntags: [a, b]\n---\nbody"
	d := Parse([]byte(src))
	if d.Err != nil {
		t.Fatalf("Err = %v", d.Err)
	}
	if d.ID != validID || d.Title != "Hello" || string(d.Body) != "body" {
		t.Errorf("doc = %+v", d)
	}
	if got := Strings(d.Meta, "tags"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("tags = %v", got)
	}
}

func TestParse_neverFails(t *testing.T) {
	tests := map[string]string{
		"bad yaml":       "---\n: : [\n---\nbody",
		"no frontmatter": "just text",
		"empty":          "",
		"scalar block":   "---\njust a string\n---\nbody",
	}
	for name, src := range tests {
		t.Run(name, func(t *testing.T) {
			d := Parse([]byte(src))
			if d.Meta == nil {
				t.Error("Meta must never be nil")
			}
		})
	}
}

func TestParse_badYAMLRecordsErr(t *testing.T) {
	d := Parse([]byte("---\n: : [\n---\nbody"))
	if d.Err == nil {
		t.Error("expected Err for bad YAML")
	}
	if string(d.Body) != "body" {
		t.Errorf("Body = %q, want body", d.Body)
	}
}

func TestParse_malformedID(t *testing.T) {
	d := Parse([]byte("---\nid: not-a-ulid\n---\nbody"))
	if !errors.Is(d.Err, ErrMalformedID) {
		t.Errorf("Err = %v, want ErrMalformedID", d.Err)
	}
	if d.ID != "" {
		t.Errorf("ID = %q, want cleared", d.ID)
	}
}

func TestParse_allDigitIDNotMangled(t *testing.T) {
	// YAML decodes this as an integer; it must still match as a string.
	id := "0000000000000000000000000000"
	d := Parse([]byte("---\nid: " + id + "\n---\n"))
	if d.ID != "" && d.ID != id {
		t.Errorf("ID = %q", d.ID)
	}
}

func TestStrings(t *testing.T) {
	meta := map[string]any{
		"list":   []any{"a", 1, "", true},
		"scalar": "solo",
		"empty":  "",
		"map":    map[string]any{"x": 1},
	}
	tests := []struct {
		key  string
		want []string
	}{
		{"list", []string{"a", "1", "true"}},
		{"scalar", []string{"solo"}},
		{"empty", nil},
		{"missing", nil},
		{"map", nil},
	}
	for _, tt := range tests {
		if got := Strings(meta, tt.key); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Strings(%q) = %#v, want %#v", tt.key, got, tt.want)
		}
	}
}

func TestStr(t *testing.T) {
	tests := []struct {
		in   any
		want string
	}{
		{nil, ""}, {"  x ", "x"}, {42, "42"}, {int64(7), "7"},
		{uint(3), "3"}, {uint64(9), "9"}, {1.5, "1.5"},
		{true, "true"}, {false, "false"}, {[]any{1}, ""},
	}
	for _, tt := range tests {
		if got := str(tt.in); got != tt.want {
			t.Errorf("str(%#v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
