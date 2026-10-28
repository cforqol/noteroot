package lock

import "testing"

func TestHashVerify(t *testing.T) {
	hash, salt, err := Hash("hunter2")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if hash == "" || salt == "" {
		t.Fatal("empty hash or salt")
	}
	if !Verify("hunter2", hash, salt) {
		t.Error("correct password rejected")
	}
	if Verify("hunter3", hash, salt) {
		t.Error("wrong password accepted")
	}
}

func TestHashUniqueSalt(t *testing.T) {
	h1, s1, _ := Hash("same-password")
	h2, s2, _ := Hash("same-password")
	if s1 == s2 {
		t.Error("salt reused across hashes")
	}
	if h1 == h2 {
		t.Error("hash reused across salts")
	}
}

func TestHashTooShort(t *testing.T) {
	if _, _, err := Hash("ab"); err != ErrTooShort {
		t.Errorf("err = %v, want ErrTooShort", err)
	}
}

func TestVerifyMalformed(t *testing.T) {
	if Verify("x", "not-base64!!", "also-bad") {
		t.Error("malformed material accepted")
	}
}

func TestTokenRoundTrip(t *testing.T) {
	tok, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if len(tok) < 20 {
		t.Errorf("token too short: %q", tok)
	}
	tokens := AddToken(AddToken(nil, "a"), tok)
	if len(tokens) != 2 {
		t.Fatalf("tokens = %v", tokens)
	}
	if again := ParseTokens(JoinTokens(tokens)); len(again) != 2 {
		t.Errorf("round trip = %v", again)
	}
	if got := AddToken(tokens, "a"); len(got) != 2 {
		t.Errorf("duplicate appended: %v", got)
	}
	if got := RemoveToken(tokens, "a"); len(got) != 1 || got[0] != tok {
		t.Errorf("remove = %v", got)
	}
}

func TestCookieName(t *testing.T) {
	if got := CookieName("ideas"); got != "nr_lock_ideas" {
		t.Errorf("CookieName = %q", got)
	}
}

func TestCoverPath(t *testing.T) {
	cases := []struct {
		session, target string
		want            bool
	}{
		{"", "anything", true},
		{"", "", true},
		{"work/", "work/a.md", true},
		{"work/", "work/sub/b.md", true},
		{"work/", "workshop/a.md", false},
		{"work/a.md", "work/a.md", true},
		{"work/a.md", "work/b.md", false},
		{"work/a.md", "", false},
	}
	for _, c := range cases {
		if got := CoverPath(c.session, c.target); got != c.want {
			t.Errorf("CoverPath(%q, %q) = %v, want %v", c.session, c.target, got, c.want)
		}
	}
}

func TestCovers(t *testing.T) {
	if Covers(nil, "") {
		t.Error("no sessions must not cover root")
	}
	if Covers([]string{"work/a.md"}, "work/b.md") {
		t.Error("unrelated session covered target")
	}
	if !Covers([]string{"work/a.md", ""}, "work/b.md") {
		t.Error("root session should cover everything")
	}
}

func TestNormalizeRel(t *testing.T) {
	cases := []struct {
		kind, rel, want string
	}{
		{"note", "a/b.md", "a/b.md"},
		{"note", "/a/b.md", "a/b.md"},
		{"folder", "a/b", "a/b/"},
		{"folder", "a/b/", "a/b/"},
		{"folder", "a/b.md", "a/b.md/"},
		{"note", "", ""},
	}
	for _, c := range cases {
		if got := NormalizeRel(c.kind, c.rel); got != c.want {
			t.Errorf("NormalizeRel(%q, %q) = %q, want %q", c.kind, c.rel, got, c.want)
		}
	}
}
