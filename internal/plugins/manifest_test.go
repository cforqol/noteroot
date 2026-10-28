package plugins

import "testing"

func TestManifest_validate(t *testing.T) {
	dir := "/tmp/plugins/good"
	valid := Manifest{Name: "good", Version: "1.0.0"}
	if _, err := valid.Validate(dir); err != nil {
		t.Errorf("valid manifest: %v", err)
	}
	if !(Manifest{Name: "x", Version: "1"}).IsEnabled() {
		t.Error("nil enabled should default true")
	}
	off := false
	if (&Manifest{Enabled: &off}).IsEnabled() {
		t.Error("enabled=false should be disabled")
	}

	bad := []struct {
		what string
		m    Manifest
	}{
		{"name mismatch", Manifest{Name: "other", Version: "1"}},
		{"bad name case", Manifest{Name: "Good", Version: "1"}},
		{"bad name ident", Manifest{Name: "1st", Version: "1"}},
		{"missing version", Manifest{Name: "good"}},
		{"bad permission", Manifest{Name: "good", Version: "1", Permissions: []string{"wat"}}},
		{"entry escape", Manifest{Name: "good", Version: "1", Entry: "../evil.lua"}},
		{"entry absolute", Manifest{Name: "good", Version: "1", Entry: "/etc/passwd"}},
		{"entry unclean (dot-dot segment)", Manifest{Name: "good", Version: "1", Entry: "sub/../x.lua"}},
	}
	for _, c := range bad {
		if _, err := c.m.Validate(dir); err == nil {
			t.Errorf("%s: accepted, want rejection", c.what)
		}
	}

	// A clean nested entry inside the directory is fine.
	nested := Manifest{Name: "good", Version: "1", Entry: "lib/init.lua"}
	abs, err := nested.Validate(dir)
	if err != nil {
		t.Errorf("nested entry: %v", err)
	}
	if abs != "/tmp/plugins/good/lib/init.lua" {
		t.Errorf("entry path = %q", abs)
	}
}
