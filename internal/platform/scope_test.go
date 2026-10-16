package platform

import "testing"

func TestParseGrants_valid(t *testing.T) {
	cases := []struct {
		spec string
		want []Grant
	}{
		{"note:read", []Grant{{Resource: "note", Action: "read"}}},
		{"*:*", []Grant{{Resource: "*", Action: "*"}}},
		{"*", []Grant{{Resource: "*", Action: "*"}}},
		{"note:read:projects/*", []Grant{{Resource: "note", Action: "read", Qualifier: "projects/*"}}},
		{"note:read search:read resources:write", []Grant{
			{Resource: "note", Action: "read"},
			{Resource: "search", Action: "read"},
			{Resource: "resources", Action: "write"},
		}},
		{"note:read,search:read", []Grant{
			{Resource: "note", Action: "read"},
			{Resource: "search", Action: "read"},
		}},
		{"", []Grant{}},
	}
	for _, c := range cases {
		got, err := ParseGrants(c.spec)
		if err != nil {
			t.Errorf("ParseGrants(%q) error = %v", c.spec, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("ParseGrants(%q) = %v, want %v", c.spec, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("ParseGrants(%q)[%d] = %+v, want %+v", c.spec, i, got[i], c.want[i])
			}
		}
	}
}

func TestParseGrants_rejectsMalformed(t *testing.T) {
	bad := []string{
		"note",             // no action
		"note:exec",        // unknown action
		"note:read:a:b",    // too many parts
		":read",            // empty resource
		"Note:read",        // bad resource case
		"note:read:[",      // bad qualifier glob
		"note:read:x note", // one bad entry fails the list
	}
	for _, spec := range bad {
		if _, err := ParseGrants(spec); err == nil {
			t.Errorf("ParseGrants(%q) error = nil, want rejection", spec)
		}
	}
}

func TestGrantsAllow_matrix(t *testing.T) {
	must := func(spec string) []Grant {
		gs, err := ParseGrants(spec)
		if err != nil {
			t.Fatalf("ParseGrants(%q): %v", spec, err)
		}
		return gs
	}
	cases := []struct {
		spec, resource, action, qualifier string
		want                              bool
	}{
		{"note:read", "note", "read", "a.md", true},
		{"note:read", "note", "write", "a.md", false},
		{"note:read", "search", "read", "", false},
		{"note:write", "note", "read", "a.md", false},
		{"*:*", "backup", "write", "", true},
		{"*", "anything", "write", "x", true},
		{"note:read", "note", "read", "", true}, // unqualified request, unqualified grant
		{"note:read:a.md", "note", "read", "a.md", true},
		{"note:read:a.md", "note", "read", "b.md", false},
		{"note:read:a.md", "note", "read", "", false}, // qualifier required by grant
		{"note:read:projects/*", "note", "read", "projects/a.md", true},
		{"note:read:projects/*", "note", "read", "projects/2024/q1.md", true}, // subtree
		{"note:read:projects/*", "note", "read", "inbox/a.md", false},
		{"note:read:*.md", "note", "read", "a.md", true},
		{"note:read:*.md", "note", "read", "a.txt", false},
	}
	for _, c := range cases {
		got := GrantsAllow(must(c.spec), c.resource, c.action, c.qualifier)
		if got != c.want {
			t.Errorf("GrantsAllow(%q, %s, %s, %q) = %v, want %v",
				c.spec, c.resource, c.action, c.qualifier, got, c.want)
		}
	}
}

func TestGrantsSpec_roundTrip(t *testing.T) {
	spec := "* note:read search:read:q1 resources:write"
	gs, err := ParseGrants(spec)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseGrants(GrantsSpec(gs))
	if err != nil {
		t.Fatal(err)
	}
	if GrantsSpec(again) != GrantsSpec(gs) {
		t.Errorf("round trip: %q -> %q", GrantsSpec(gs), GrantsSpec(again))
	}
}
