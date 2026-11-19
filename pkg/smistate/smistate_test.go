package smistate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/mesutoezdil/fake-gpu/pkg/fleet"
	"github.com/mesutoezdil/fake-gpu/pkg/profile"
)

func gpus(t *testing.T, slug string) []fleet.GPU {
	t.Helper()
	p, err := profile.Load("../../profiles/" + slug + ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	return fleet.FromProfile(p, slug)
}

func TestRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "smi")
	all := gpus(t, "mi300x")
	if err := Sync(dir, all); err != nil {
		t.Fatal(err)
	}
	want := all[3]
	want.Status, want.Throttled, want.RetiredPages, want.PowerW, want.TempC, want.ECCErrors = fleet.PageRetired, true, 4, 321, 72, 7
	if err := Write(dir, &want); err != nil {
		t.Fatal(err)
	}
	got, err := Read(dir)
	if err != nil || len(got) != len(all) {
		t.Fatalf("%v, %d GPUs", err, len(got))
	}
	g := got[3]
	if g.Status != want.Status || !g.Throttled || g.RetiredPages != 4 || g.PowerW != 321 || g.TempC != 72 || g.ECCErrors != 7 ||
		g.Name != want.Name || g.BDF != want.BDF || g.UUID != UUID(want.UUID) || g.MemTotalMB != want.MemTotalMB {
		t.Fatalf("round trip lost state: %+v", g)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "gpu3"))
	if rows := strings.Split(strings.TrimSpace(string(data)), "\n"); len(rows) != 5 || !strings.HasPrefix(rows[0], "72 321 ") {
		t.Fatalf("snapshot layout %q", data)
	}
	if tmp, _ := filepath.Glob(filepath.Join(dir, ".*")); len(tmp) != 0 {
		t.Fatalf("temporary files left: %v", tmp)
	}
}

// A collector keeps the directory mounted while the agent re-renders
// another profile: the inode stays and GPUs that are gone disappear.
func TestSyncKeepsTheDirectoryAndDropsStaleGPUs(t *testing.T) {
	dir := t.TempDir()
	if err := Sync(dir, gpus(t, "mi250x")); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(dir)
	if err := os.WriteFile(filepath.Join(dir, "unrelated"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Sync(dir, gpus(t, "mi300a")); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(dir)
	if !os.SameFile(before, after) {
		t.Fatal("directory replaced")
	}
	names, _ := filepath.Glob(filepath.Join(dir, "gpu*"))
	if len(names) != 4 {
		t.Fatalf("GPU files after shrinking to four: %v", names)
	}
	if _, err := os.Stat(filepath.Join(dir, "unrelated")); err != nil {
		t.Fatal("a file that is not a GPU snapshot was removed")
	}
	if count, _ := os.ReadFile(filepath.Join(dir, "count")); string(count) != "4\n" {
		t.Fatalf("count %q", count)
	}
}

func TestOlderSnapshotsWithoutStatus(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "count"), []byte("1\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "gpu0"), []byte("40 100 750 5 300 196608 0 500 1300\nX\nU\n0000:05:00.0\n"), 0o644)
	g, err := Read(dir)
	if err != nil || len(g) != 1 || g[0].Status != fleet.Healthy || g[0].TempC != 40 || g[0].Name != "X" {
		t.Fatalf("%v %+v", err, g)
	}
}

func TestReadErrors(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"no count":    {},
		"bad count":   {"count": "x\n"},
		"missing gpu": {"count": "1\n"},
		"truncated":   {"count": "1\n", "gpu0": "1 2 3\nname\n"},
		"bad numbers": {"count": "1\n", "gpu0": "a b c d e f g h i\nn\nu\nb\n"},
		"bad status":  {"count": "1\n", "gpu0": "1 2 3 4 5 6 7 8 9\nn\nu\nb\nhealthy x\n"},
	} {
		dir := t.TempDir()
		for f, body := range files {
			os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644)
		}
		if _, err := Read(dir); err == nil {
			t.Errorf("%s: read succeeded", name)
		}
	}
}

// AMD GPU Agent parses the UUID as RFC 4122: version 8, variant 10.
func TestUUID(t *testing.T) {
	u := UUID("GPU-mi300x-0")
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-8[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(u) {
		t.Fatalf("%q", u)
	}
	if UUID("GPU-mi300x-0") != u || UUID("GPU-mi300x-1") == u {
		t.Fatal("UUIDs must be stable and distinct")
	}
}
