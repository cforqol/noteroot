package vault

import "testing"

func TestRel(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "simple file", input: "foo.md", want: "foo.md"},
		{name: "nested", input: "a/b/c.md", want: "a/b/c.md"},
		{name: "trim leading space", input: "  foo.md  ", want: "foo.md"},
		{name: "backslash to slash", input: `a\b\c.md`, want: "a/b/c.md"},
		{name: "normalize redundant dot", input: "a/./b.md", want: "a/b.md"},
		{name: "trailing slash cleaned", input: "foo/", want: "foo"},
		{name: "double slash cleaned", input: "a//b.md", want: "a/b.md"},
		{name: "unicode NFC", input: "caf\u0065\u0301.md", want: "caf\u00e9.md"},
		{name: "empty", input: "", wantErr: true},
		{name: "whitespace only", input: "   ", wantErr: true},
		{name: "absolute path", input: "/foo.md", wantErr: true},
		{name: "dot dot", input: "..", wantErr: true},
		{name: "dot dot slash prefix", input: "../foo.md", wantErr: true},
		{name: "escape via subdir", input: "a/../../foo.md", wantErr: true},
		{name: "dot alone", input: ".", wantErr: true},
		{name: "dot slash", input: "./foo.md", want: "foo.md"},
		{name: "control char NUL", input: "foo\x00bar.md", wantErr: true},
		{name: "control char SOH", input: "foo\x01bar.md", wantErr: true},
		{name: "DEL char", input: "foo\x7fbar.md", wantErr: true},
		{name: "trailing dot in segment", input: "foo./bar.md", wantErr: true},
		{name: "trailing space in segment", input: "foo /bar.md", wantErr: true},
		{name: "reserved CON", input: "CON", wantErr: true},
		{name: "reserved con lowercase", input: "con", wantErr: true},
		{name: "reserved CON.md", input: "CON.md", wantErr: true},
		{name: "reserved NUL.txt", input: "NUL.txt", wantErr: true},
		{name: "reserved COM1", input: "COM1", wantErr: true},
		{name: "reserved LPT9", input: "LPT9.md", wantErr: true},
		{name: "reserved PRN in subdir", input: "notes/PRN.md", wantErr: true},
		{name: "non-reserved CONX", input: "CONX.md", want: "CONX.md"},
		{name: "non-reserved COM10", input: "COM10.md", want: "COM10.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Rel(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Rel(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("Rel(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIgnored(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"foo.md", false},
		{"a/b/foo.md", false},
		{".hidden", true},
		{".git", true},
		{"a/.hidden", true},
		{"a/.git", true},
		{"foo.swp", true},
		{"a/foo.swp", true},
		{"foo.swo", true},
		{"foo~", true},
		{"foo.tmp", true},
		{"foo.md.tmp", true},
		{"foo.md~", true},
		{"foo.md.swp", true},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := Ignored(tt.path); got != tt.want {
				t.Errorf("Ignored(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsNote(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"foo.md", true},
		{"a/b/c.md", true},
		{".hidden.md", false},
		{"foo.txt", false},
		{"foo.md.tmp", false},
		{"foo.md~", false},
		{"foo.md.swp", false},
		{"foo.md.swo", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := IsNote(tt.path); got != tt.want {
				t.Errorf("IsNote(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}
