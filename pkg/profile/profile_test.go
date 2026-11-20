package profile

import (
	"fmt"
	"strings"
	"testing"
)

func catalog(t *testing.T) *Catalog {
	t.Helper()
	c, err := LoadDir("../../profiles")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Slugs) != 7 {
		t.Fatalf("want the seven shipped profiles, got %v", c.Slugs)
	}
	return c
}

// Every shipped profile has to describe a node the renderer and AMD's
// discovery can use: unique identities, cards in index order, and each card
// behind exactly one root complex.
func TestShippedProfilesAreConsistent(t *testing.T) {
	for slug, p := range catalog(t).Profiles {
		t.Run(slug, func(t *testing.T) {
			if p.Card.Vendor != 0x1002 || p.Card.Name == "" || p.Card.Partition.Compute != "SPX" {
				t.Fatalf("card %+v", p.Card)
			}
			if p.Card.Power.MaxCap == 0 || p.Card.Memory.VRAM == 0 || p.Card.Compute.SIMDs == 0 {
				t.Fatal("power cap, VRAM and SIMDs drive the simulator and must be set")
			}
			seen := map[string]bool{}
			for i, d := range p.Devices {
				if d.Index != i || d.Slice != 0 {
					t.Fatalf("device %d has index %d slice %d", i, d.Index, d.Slice)
				}
				for _, id := range []string{"uuid:" + d.UUID, "bdf:" + d.BDF, "unique:" + d.UniqueID, fmt.Sprint("minor:", d.RenderMinor)} {
					if seen[id] {
						t.Fatalf("duplicate %s", id)
					}
					seen[id] = true
				}
				if _, err := ParseID(d.UniqueID); err != nil {
					t.Fatal(err)
				}
				roots := 0
				for _, rc := range p.Topology.RootComplexes {
					for _, b := range rc.Devices {
						if b == d.BDF {
							roots++
						}
					}
				}
				if roots != 1 {
					t.Fatalf("%s sits behind %d root complexes", d.BDF, roots)
				}
			}
			if _, err := ParseID(p.XGMI.Hive); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPartition(t *testing.T) {
	c := catalog(t)
	spx := c.Profiles["mi300x"]
	before := *spx

	same, err := Partition(spx, "spx")
	if err != nil || len(same.Devices) != len(spx.Devices) || same.Card.Partition.Compute != "SPX" {
		t.Fatalf("SPX: %v", err)
	}
	dpx, err := Partition(spx, "DPX")
	if err != nil {
		t.Fatal(err)
	}
	if len(dpx.Devices) != 16 || dpx.Card.Partition.Memory != "NPS2" || dpx.Card.Partition.Compute != "DPX" {
		t.Fatalf("DPX shape: %d devices, %+v", len(dpx.Devices), dpx.Card.Partition)
	}
	if dpx.Card.Memory.VRAM*2 != spx.Card.Memory.VRAM || dpx.Card.Compute.CUs*2 != spx.Card.Compute.CUs || dpx.Card.Power.MaxCap*2 != spx.Card.Power.MaxCap {
		t.Fatal("capacity not split in two")
	}
	for i, d := range dpx.Devices {
		card := spx.Devices[i/2]
		if d.Index != i || d.Slice != i%2 || d.RenderMinor != 128+i || d.BDF != card.BDF || d.UniqueID != card.UniqueID || !strings.HasPrefix(d.UUID, card.UUID+"-part") {
			t.Fatalf("slice %d: %+v of %+v", i, d, card)
		}
	}
	if spx.Card != before.Card || len(spx.Devices) != 8 {
		t.Fatal("the input profile changed")
	}

	for _, mode := range []string{"CPX", "QPX", "", "bogus"} {
		if _, err := Partition(spx, mode); err == nil {
			t.Errorf("mode %q accepted", mode)
		}
	}
	if _, err := Partition(c.Profiles["mi210"], "DPX"); err == nil {
		t.Error("DPX accepted for MI210")
	}
	if _, err := Partition(dpx, "DPX"); err == nil {
		t.Error("an already partitioned profile was split again")
	}
}

func TestLookups(t *testing.T) {
	p := catalog(t).Profiles["mi300x"]
	d := p.Devices[3]
	if rc, ok := p.Root(d.BDF); !ok || p.NUMA(d.BDF) != rc.NUMA {
		t.Fatal("root complex lookup")
	}
	if p.NUMA("ffff:ff:1f.7") != -1 {
		t.Fatal("unknown address must have no NUMA node")
	}
	if p.RenderMinor(3) != d.RenderMinor || p.RenderMinor(40) != 168 {
		t.Fatal("render minor lookup")
	}
	if got := (&Card{Name: "A"}).ProductName(); got != "A" {
		t.Fatal("product name falls back to the name")
	}
	if got := (&Card{Name: "A", Product: "A OAM"}).ProductName(); got != "A OAM" {
		t.Fatal("product name")
	}
}

func TestParseID(t *testing.T) {
	for in, want := range map[string]uint64{"": 0, "0": 0, "42": 42, "0x2a": 42, "18446744073709551615": 1<<64 - 1} {
		if got, err := ParseID(in); err != nil || got != want {
			t.Errorf("ParseID(%q) = %d, %v", in, got, err)
		}
	}
	for _, bad := range []string{"x", "-1", "0x", "18446744073709551616"} {
		if _, err := ParseID(bad); err == nil {
			t.Errorf("ParseID(%q) accepted", bad)
		}
	}
}

func TestParseBDF(t *testing.T) {
	b := ParseBDF("0001:c3:1a.5")
	if b != (BDF{1, 0xc3, 0x1a, 5}) || b.Location() != 0xc3<<8|0x1a<<3|5 {
		t.Fatalf("%+v", b)
	}
	if ParseBDF("garbage") != (BDF{}) {
		t.Fatal("a malformed address must parse to zero")
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := Load("../../profiles/none.yaml"); err == nil {
		t.Fatal("missing file loaded")
	}
	if _, err := LoadDir("../../no-such-dir"); err == nil {
		t.Fatal("missing directory loaded")
	}
}
