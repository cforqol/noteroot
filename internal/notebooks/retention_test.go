package notebooks

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func retentionFixture(t *testing.T, extra string) (*Registry, Layout) {
	t.Helper()
	dataDir := t.TempDir()
	root := filepath.Join(dataDir, "notebooks", "work")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "id = \"abc\"\nslug = \"work\"\nname = \"Work\"\nprofile = \"plaintext\"\n" +
		"layout_version = 1\nquota_mb = 0\ncreated = 2026-01-01T00:00:00Z\n" + extra
	if err := os.WriteFile(filepath.Join(root, ManifestFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, problems := Open(dataDir)
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	return reg, NewLayout(root)
}

func TestRetentionDays_defaultNeverAndExplicit(t *testing.T) {
	zero, thirty, seven, negative := 0, 30, 7, -5
	cases := []struct {
		name string
		set  *int
		want int
	}{
		{"silent manifest uses the default", nil, DefaultTrashRetentionDays},
		{"zero means never", &zero, RetentionNever},
		{"a negative value is clamped to never", &negative, RetentionNever},
		{"an explicit value is kept", &seven, 7},
		{"the default may be written out", &thirty, 30},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Manifest{TrashRetentionDays: c.set}
			if got := m.RetentionDays(); got != c.want {
				t.Errorf("RetentionDays() = %d, want %d", got, c.want)
			}
		})
	}
}

func TestTrashCutoff(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	seven := 7
	cutoff, ok := Manifest{TrashRetentionDays: &seven}.TrashCutoff(now)
	if !ok {
		t.Fatal("TrashCutoff(7 days) reported no cutoff")
	}
	if want := now.AddDate(0, 0, -7); !cutoff.Equal(want) {
		t.Errorf("cutoff = %s, want %s", cutoff, want)
	}

	never := 0
	if _, ok := (Manifest{TrashRetentionDays: &never}).TrashCutoff(now); ok {
		t.Error("TrashCutoff(never) reported a cutoff")
	}

	// A silent manifest sweeps at the default.
	if _, ok := (Manifest{}).TrashCutoff(now); !ok {
		t.Error("default retention reported no cutoff")
	}
}

func TestSetTrashRetention_persistsAndSwapsNotebook(t *testing.T) {
	reg, l := retentionFixture(t, "")
	before, _ := reg.Get("work")
	if got := before.Manifest.RetentionDays(); got != DefaultTrashRetentionDays {
		t.Fatalf("initial RetentionDays() = %d", got)
	}

	nb, err := reg.SetTrashRetention("work", 7)
	if err != nil {
		t.Fatalf("SetTrashRetention: %v", err)
	}
	if got := nb.Manifest.RetentionDays(); got != 7 {
		t.Errorf("returned RetentionDays() = %d, want 7", got)
	}
	if again, _ := reg.Get("work"); again.Manifest.RetentionDays() != 7 {
		t.Error("registry still serves the old retention")
	}
	// The caller's old pointer must not have been mutated under it.
	if got := before.Manifest.RetentionDays(); got != DefaultTrashRetentionDays {
		t.Errorf("previously held notebook changed to %d", got)
	}

	// It survives a reload, and "never" round-trips as an explicit 0.
	var raw map[string]any
	if _, err := toml.DecodeFile(l.Manifest(), &raw); err != nil {
		t.Fatal(err)
	}
	if raw[trashRetentionKey] != int64(7) {
		t.Errorf("manifest %s = %v, want 7", trashRetentionKey, raw[trashRetentionKey])
	}
	if _, err := reg.SetTrashRetention("work", RetentionNever); err != nil {
		t.Fatal(err)
	}
	reloaded, problems := Open(filepath.Dir(filepath.Dir(l.Root())))
	if len(problems) != 0 {
		t.Fatalf("reload problems: %v", problems)
	}
	back, _ := reloaded.Get("work")
	if got := back.Manifest.RetentionDays(); got != RetentionNever {
		t.Errorf("reloaded RetentionDays() = %d, want never", got)
	}
}

func TestSetTrashRetention_keepsUnknownManifestKeys(t *testing.T) {
	reg, l := retentionFixture(t, "custom_key = \"keep me\"\n")
	if _, err := reg.SetTrashRetention("work", 14); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if _, err := toml.DecodeFile(l.Manifest(), &raw); err != nil {
		t.Fatal(err)
	}
	if raw["custom_key"] != "keep me" {
		t.Errorf("unknown key lost: %v", raw["custom_key"])
	}
	if raw["slug"] != "work" || raw["id"] != "abc" {
		t.Errorf("known keys mangled: %v", raw)
	}
	if _, ok := raw["created"]; !ok {
		t.Error("created timestamp lost")
	}
}

func TestSetTrashRetention_rejectsBadInput(t *testing.T) {
	reg, _ := retentionFixture(t, "")
	if _, err := reg.SetTrashRetention("work", -1); err != ErrBadRetention {
		t.Errorf("negative days err = %v, want ErrBadRetention", err)
	}
	if _, err := reg.SetTrashRetention("work", MaxTrashRetentionDays+1); err != ErrBadRetention {
		t.Errorf("huge days err = %v, want ErrBadRetention", err)
	}
	if _, err := reg.SetTrashRetention("nope", 7); err != ErrNoNotebook {
		t.Errorf("unknown slug err = %v, want ErrNoNotebook", err)
	}
	// A rejected call leaves the manifest untouched.
	nb, _ := reg.Get("work")
	if got := nb.Manifest.RetentionDays(); got != DefaultTrashRetentionDays {
		t.Errorf("RetentionDays() = %d after failures", got)
	}
}

func TestSetTrashRetention_concurrentReadersSeeAConsistentManifest(t *testing.T) {
	reg, _ := retentionFixture(t, "")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 1; i <= 50; i++ {
			if _, err := reg.SetTrashRetention("work", i); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for i := 0; i < 200; i++ {
		nb, ok := reg.Get("work")
		if !ok {
			t.Fatal("notebook vanished mid-update")
		}
		if d := nb.Manifest.RetentionDays(); d < 0 || d > MaxTrashRetentionDays {
			t.Fatalf("torn retention value %d", d)
		}
		reg.List()
	}
	<-done
}
