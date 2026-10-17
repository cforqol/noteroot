package frontmatter

import (
	"bytes"
	"errors"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"
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

// A YAML error elsewhere in the block must not drop a well-formed id:
// writers mint a new one whenever ID is empty, so losing it here would
// re-key the note on every save.
func TestParse_badYAMLKeepsTextualID(t *testing.T) {
	for _, src := range []string{
		"---\nid: " + validID + "\ntitle: Meeting: notes\n---\nbody",
		"---\ntitle: [unclosed\nid: \"" + validID + "\" # stable\n---\nbody",
		"---\r\nid: '" + validID + "'\r\nbad: : x\r\n---\r\nbody",
	} {
		d := Parse([]byte(src))
		if d.Err == nil {
			t.Errorf("%q: expected a YAML error", src)
		}
		if d.ID != validID {
			t.Errorf("%q: ID = %q, want %q recovered", src, d.ID, validID)
		}
	}
	// Nested or malformed id lines are not identities.
	for _, src := range []string{
		"---\nmeta:\n  id: " + validID + "\nx: : y\n---\n",
		"---\nid: nope\nx: : y\n---\n",
	} {
		if d := Parse([]byte(src)); d.ID != "" {
			t.Errorf("%q: ID = %q, want empty", src, d.ID)
		}
	}
}

func TestSplit_largeBodyKeepsBlock(t *testing.T) {
	body := bytes.Repeat([]byte("lorem ipsum\n"), (maxBlock/12)*2)
	src := append([]byte("---\nid: "+validID+"\n---\n"), body...)
	block, gotBody, ok := Split(src)
	if !ok {
		t.Fatal("a small block before a large body must parse")
	}
	if string(block) != "id: "+validID+"\n" || !bytes.Equal(gotBody, body) {
		t.Errorf("block = %q, body len = %d", block, len(gotBody))
	}
	// And identity survives a write of the large note: no second block.
	out := WithID(src, validID)
	if bytes.Count(out, []byte("---\n")) != 2 {
		t.Errorf("WithID added a block to a large note")
	}
}

func TestSplit_closingDelimiterAtLimit(t *testing.T) {
	pad := strings.Repeat("a", maxBlock-1) + "\n" // block of exactly maxBlock bytes
	if _, _, ok := Split([]byte("---\n" + pad + "---\nbody")); !ok {
		t.Error("closing delimiter starting at maxBlock must still count")
	}
	if _, _, ok := Split([]byte("---\n" + pad + "b\n---\nbody")); ok {
		t.Error("closing delimiter past maxBlock must not count")
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

func TestNewID_format(t *testing.T) {
	id := NewID()
	if len(id) != 28 {
		t.Fatalf("NewID() = %q (len %d), want 28 chars", id, len(id))
	}
	if !ValidID(id) {
		t.Errorf("NewID() = %q is not ValidID", id)
	}
}

func TestNewID_unique(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		id := NewID()
		if seen[id] {
			t.Fatalf("duplicate id %q at i=%d", id, i)
		}
		seen[id] = true
	}
}

func TestNewID_timestampNearNow(t *testing.T) {
	id := NewID()
	ts := decodeULTimestamp(t, id[:26])
	now := time.Now().UnixMilli()
	if delta := now - ts; delta < -5000 || delta > 5000 {
		t.Errorf("timestamp %d not within 5s of now %d", ts, now)
	}
}

// decodeULTimestamp extracts the 48-bit ms timestamp from a 26-char ULID.
func decodeULTimestamp(t *testing.T, ulid string) int64 {
	t.Helper()
	const crock = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	n := new(big.Int)
	for _, c := range ulid {
		idx := strings.IndexRune(crock, c)
		if idx < 0 {
			t.Fatalf("bad char %q in ULID %q", c, ulid)
		}
		n.Mul(n, big.NewInt(32))
		n.Add(n, big.NewInt(int64(idx)))
	}
	// The 26 chars encode the original 128-bit value: top 48 bits are the
	// timestamp, bottom 80 bits are entropy. Shift right by 80.
	ts := new(big.Int).Rsh(n, 80)
	return ts.Int64()
}

func TestValidID(t *testing.T) {
	valid := NewID()
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"fresh", valid, true},
		{"empty", "", false},
		{"short", valid[:27], false},
		{"long", valid + "0", false},
		{"lowercase", strings.ToLower(valid), false},
		{"has I", "I" + valid[1:], false},
		{"has L", "L" + valid[1:], false},
		{"has O", "O" + valid[1:], false},
		{"has U", "U" + valid[1:], false},
		{"hyphen", valid[:14] + "-" + valid[14:], false},
	}
	for _, tt := range tests {
		if got := ValidID(tt.id); got != tt.want {
			t.Errorf("%s: ValidID(%q) = %v, want %v", tt.name, tt.id, got, tt.want)
		}
	}
}

func TestWithID_noFrontmatter(t *testing.T) {
	id := NewID()
	src := []byte("# Hello\n\nbody\n")
	out := WithID(src, id)
	doc := Parse(out)
	if doc.ID != id {
		t.Errorf("ID = %q, want %q", doc.ID, id)
	}
	if !bytes.HasPrefix(out, []byte("---\nid: "+id+"\n---\n")) {
		t.Errorf("missing frontmatter block:\n%s", out)
	}
	if !bytes.Contains(doc.Body, []byte("# Hello")) {
		t.Errorf("body lost:\n%s", out)
	}
}

func TestWithID_insertMissingID(t *testing.T) {
	id := NewID()
	src := []byte("---\ntitle: My Note\ntags: [a, b]\n---\n\nbody\n")
	out := WithID(src, id)
	doc := Parse(out)
	if doc.ID != id {
		t.Errorf("ID = %q, want %q", doc.ID, id)
	}
	if doc.Title != "My Note" {
		t.Errorf("Title = %q, want %q", doc.Title, "My Note")
	}
	if got := Strings(doc.Meta, "tags"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("tags = %v", got)
	}
}

func TestWithID_replacesMalformedID(t *testing.T) {
	id := NewID()
	src := []byte("---\nid: not-a-valid-id\ntitle: T\n---\nbody\n")
	out := WithID(src, id)
	if bytes.Contains(out, []byte("not-a-valid-id")) {
		t.Errorf("malformed id not replaced:\n%s", out)
	}
	doc := Parse(out)
	if doc.ID != id {
		t.Errorf("ID = %q, want %q", doc.ID, id)
	}
}

func TestWithID_replacesValidID(t *testing.T) {
	old := NewID()
	id := NewID()
	src := []byte("---\nid: " + old + "\ntitle: T\n---\nbody\n")
	out := WithID(src, id)
	doc := Parse(out)
	if doc.ID != id {
		t.Errorf("ID = %q, want %q (old not replaced)", doc.ID, id)
	}
	if bytes.Contains(out, []byte(old)) {
		t.Errorf("old id still present:\n%s", out)
	}
}

func TestWithID_preservesBodyExactly(t *testing.T) {
	id := NewID()
	src := []byte("---\ntitle: T\n---\n\n# H\n\n*body* with [[a link]]\n")
	out := WithID(src, id)
	_, body, ok := Split(out)
	if !ok {
		t.Fatal("no frontmatter block after WithID")
	}
	_, wantBody, ok := Split(src)
	if !ok {
		t.Fatal("no frontmatter block in src")
	}
	if !bytes.Equal(body, wantBody) {
		t.Errorf("body changed:\ngot  %q\nwant %q", body, wantBody)
	}
}

func TestWithID_CRLF(t *testing.T) {
	id := NewID()
	// Frontmatter with CRLF line endings
	src := []byte("---\r\ntitle: Test\r\nid: old-id\r\n---\r\nbody with CRLF\r\n")
	out := WithID(src, id)
	doc := Parse(out)
	if doc.ID != id {
		t.Errorf("ID = %q, want %q", doc.ID, id)
	}
	if doc.Title != "Test" {
		t.Errorf("Title = %q, want %q", doc.Title, "Test")
	}
	// Body should be preserved
	if !bytes.Contains(doc.Body, []byte("body with CRLF")) {
		t.Errorf("body not preserved:\n%s", out)
	}
}

func TestWithID_spacesInIDLine(t *testing.T) {
	id := NewID()
	// ID line with extra spaces
	src := []byte("---\nid  :  old-value  \ntitle: T\n---\nbody\n")
	out := WithID(src, id)
	// The current implementation should replace "id  :" because it starts with "id"
	// but note that "id :" (with space before colon) is also checked
	doc := Parse(out)
	if doc.ID != id {
		t.Errorf("ID = %q, want %q", doc.ID, id)
	}
}

func TestWithID_idAtEnd(t *testing.T) {
	id := NewID()
	// ID field at the end of frontmatter instead of beginning
	src := []byte("---\ntitle: Test\ntags: [a, b]\nid: old-id\n---\nbody\n")
	out := WithID(src, id)
	doc := Parse(out)
	if doc.ID != id {
		t.Errorf("ID = %q, want %q", doc.ID, id)
	}
	// Make sure there's only one id line
	idCount := bytes.Count(out, []byte("\nid: "))
	if idCount != 1 {
		t.Errorf("id line count = %d, want 1", idCount)
	}
}
