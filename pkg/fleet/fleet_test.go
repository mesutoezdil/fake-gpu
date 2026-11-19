package fleet

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/mesutoezdil/fake-gpu/pkg/profile"
)

func profiles(t *testing.T) *profile.Catalog {
	t.Helper()
	c, err := profile.LoadDir("../../profiles")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func mi300x(t *testing.T) (*Fleet, *profile.Profile) {
	t.Helper()
	c := profiles(t)
	f := New(c.Profiles["mi300x"], "mi300x")
	f.AddCatalog(c)
	return f, c.Profiles["mi300x"]
}

func must(t *testing.T) func(GPU, error) GPU {
	return func(g GPU, err error) GPU {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
}

func TestBootState(t *testing.T) {
	f, p := mi300x(t)
	gpus, sys := f.All()
	if len(gpus) != 8 || sys.Profile != p.Card.Name || sys.Driver != p.Software.Driver || sys.Node != "mock-node" {
		t.Fatalf("%d GPUs, %+v", len(gpus), sys)
	}
	g := gpus[2]
	if g.Status != Healthy || g.BDF != p.Devices[2].BDF || g.NUMA != p.NUMA(g.BDF) || g.MemTotalMB != int(p.Card.Memory.VRAM>>20) || g.Profile != "mi300x" {
		t.Fatalf("%+v", g)
	}
	if _, ok := f.Get(8); ok {
		t.Fatal("GPU 8 of 8")
	}
	if _, ok := f.Get(-1); ok {
		t.Fatal("GPU -1")
	}
}

func TestFaultsToggle(t *testing.T) {
	f, p := mi300x(t)
	ok := must(t)
	idle := func(g GPU) bool {
		return g.Status == Healthy && g.Util == 0 && g.ECCErrors == 0 && g.RetiredPages == 0 && !g.Throttled &&
			g.TempC == int(p.Card.Thermal.Edge) && g.PowerW == int(p.Card.Power.Socket)
	}
	for _, step := range []struct {
		name string
		do   func() (GPU, error)
		want func(GPU) bool
	}{
		{"crash", func() (GPU, error) { return f.Crash(0) }, func(g GPU) bool { return g.Status == Crashed && g.PowerW == 0 && g.GFXClockMHz == 0 }},
		{"crash again recovers", func() (GPU, error) { return f.Crash(0) }, idle},
		{"overheat", func() (GPU, error) { return f.Overheat(0, 99) }, func(g GPU) bool { return g.Status == Overheating && g.TempC == 99 }},
		{"overheat again recovers", func() (GPU, error) { return f.Overheat(0, 99) }, idle},
		{"ecc", func() (GPU, error) { return f.ECC(0, 3) }, func(g GPU) bool { return g.Status == ECCError && g.ECCErrors == 3 }},
		{"ecc again recovers", func() (GPU, error) { return f.ECC(0, 3) }, idle},
		{"throttle", func() (GPU, error) { return f.Throttle(0) }, func(g GPU) bool { return g.Status == Throttling && g.Throttled }},
		{"retire after throttle", func() (GPU, error) { return f.RetirePages(0, 5) }, func(g GPU) bool { return g.Status == PageRetired && g.RetiredPages == 5 && g.Throttled }},
		{"recover", func() (GPU, error) { return f.Recover(0) }, idle},
		{"xgmi", func() (GPU, error) { return f.XGMIDown(0) }, func(g GPU) bool { return g.Status == XGMIDown }},
		{"xgmi again recovers", func() (GPU, error) { return f.XGMIDown(0) }, idle},
	} {
		if g := ok(step.do()); !step.want(g) {
			t.Fatalf("%s: %+v", step.name, g)
		}
	}
}

func TestCrashedGPURefusesFaults(t *testing.T) {
	f, _ := mi300x(t)
	must(t)(f.Crash(1))
	for name, do := range map[string]func() (GPU, error){
		"throttle": func() (GPU, error) { return f.Throttle(1) },
		"retire":   func() (GPU, error) { return f.RetirePages(1, 1) },
		"xgmi":     func() (GPU, error) { return f.XGMIDown(1) },
	} {
		g, err := do()
		if !errors.Is(err, ErrCrashed) || g.Status != Crashed {
			t.Fatalf("%s on a crashed GPU: %v %+v", name, err, g)
		}
	}
	if _, err := f.Throttle(99); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestBusyTogglesAndScales(t *testing.T) {
	f, _ := mi300x(t)
	g := must(t)(f.Busy(2, 40, 10))
	if g.Util != 40 || g.TargetUtil != 40 || g.MemUsedMB != g.MemTotalMB/10 || g.PowerW != g.PowerCapW*40/100 || g.GFXClockMHz != 2100 {
		t.Fatalf("busy: %+v", g)
	}
	// At 50% or below busy changes the load; above it, busy means idle.
	if g = must(t)(f.Busy(2, 60, 70)); g.Util != 60 {
		t.Fatalf("busy from 40%%: %+v", g)
	}
	if g = must(t)(f.Busy(2, 60, 70)); g.Util != 0 || g.TargetUtil != 0 {
		t.Fatalf("busy from 60%% must go idle: %+v", g)
	}
}

func TestPatch(t *testing.T) {
	f, _ := mi300x(t)
	g := must(t)(f.Patch(4, json.RawMessage(`{"temperature_c":77,"status":"throttling","throttled":true}`)))
	if g.TempC != 77 || g.Status != Throttling || !g.Throttled || g.Index != 4 {
		t.Fatalf("%+v", g)
	}
	if _, err := f.Patch(4, json.RawMessage(`{"temperature_c":"hot","power_w":1}`)); err == nil {
		t.Fatal("a mistyped field was accepted")
	}
	if g, _ := f.Get(4); g.PowerW == 1 || g.TempC != 77 {
		t.Fatalf("a rejected patch changed the GPU: %+v", g)
	}
	if _, err := f.Patch(40, json.RawMessage(`{}`)); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

// Recovery returns to the profile the node booted, so a DPX slice goes back
// to half a card, not to a whole one from the catalog file.
func TestRecoveryUsesTheRunningProfile(t *testing.T) {
	c := profiles(t)
	dpx, err := profile.Partition(c.Profiles["mi300x"], "DPX")
	if err != nil {
		t.Fatal(err)
	}
	f := New(dpx, "mi300x")
	f.AddCatalog(c)
	g := must(t)(f.Recover(1))
	if g.PowerW != int(dpx.Card.Power.Socket) || g.MemUsedMB != int(dpx.Card.Memory.VRAMUsed>>20) {
		t.Fatalf("recovered to %d W, %d MB; DPX idles at %d W, %d MB", g.PowerW, g.MemUsedMB, dpx.Card.Power.Socket, dpx.Card.Memory.VRAMUsed>>20)
	}
	if cat := f.Catalog(); len(cat) != 7 || cat[3].Slug != "mi300x" || cat[3].GPUs != 8 || cat[3].MemoryGB != 192 || cat[3].TDPW != 750 {
		t.Fatalf("catalog %+v", cat)
	}

	// A GPU whose profile is unknown recovers to MI300X values.
	must(t)(f.Patch(0, json.RawMessage(`{"profile_slug":"gone"}`)))
	if g := must(t)(f.Recover(0)); g.TempC != 49 || g.PowerW != 183 || g.MemUsedMB != 300 {
		t.Fatalf("%+v", g)
	}
}

func TestSwitch(t *testing.T) {
	f, _ := mi300x(t)
	gpus, p, ok := f.Switch("mi250x")
	if !ok || len(gpus) != 16 || p.Card.Name != "AMD Instinct MI250X" {
		t.Fatalf("%d GPUs, %v", len(gpus), ok)
	}
	if _, sys := f.All(); sys.Profile != p.Card.Name || sys.Driver != p.Software.Driver {
		t.Fatalf("%+v", sys)
	}
	if _, _, ok := f.Switch("nope"); ok {
		t.Fatal("unknown profile")
	}
}

func TestTrays(t *testing.T) {
	c := profiles(t)
	f := New(c.Profiles["mi250x"], "mi250x")
	f.AddCatalog(c)
	trays := f.Trays()
	if len(trays) != 2 || trays[1].First != 8 || trays[1].Last != 15 || trays[1].GPUs != 8 {
		t.Fatalf("%+v", trays)
	}
	tray, ok := f.SwitchTray(9, "mi210")
	if !ok || len(tray) != 8 || tray[0].Index != 8 || tray[0].Name != "AMD Instinct MI210" || tray[0].Profile != "mi210" {
		t.Fatalf("%+v", tray)
	}
	if trays = f.Trays(); trays[0].Profile != "mi250x" || trays[1].Profile != "mi210" {
		t.Fatalf("%+v", trays)
	}
	if _, ok := f.SwitchTray(16, "mi210"); ok {
		t.Fatal("GPU 16 of 16")
	}
	if _, ok := f.SwitchTray(0, "nope"); ok {
		t.Fatal("unknown profile")
	}
	if TraySize(4) != 4 || TraySize(8) != 8 || TraySize(16) != 8 {
		t.Fatal("tray size")
	}
	if Splittable("AMD Instinct MI300X") || !Splittable("AMD Instinct MI210") {
		t.Fatal("OAM baseboards cannot be split")
	}
}

// Virtual partitions are derived from the boot profile every time, so modes
// never compound and every entry keeps its card's PCI identity.
func TestModes(t *testing.T) {
	f, p := mi300x(t)
	for _, name := range []string{"DPX", "CPX", "QPX", "SPX", "CPX", "SPX"} {
		m, ok := LookupMode(name)
		if !ok {
			t.Fatal(name)
		}
		f.SetMode(m)
		gpus, _ := f.All()
		if len(gpus) != 8*m.Slices {
			t.Fatalf("%s: %d entries", name, len(gpus))
		}
		uuids := map[string]bool{}
		for i, g := range gpus {
			if g.Index != i || g.Partition != name || g.BDF != p.Devices[i/m.Slices].BDF || uuids[g.UUID] ||
				g.MemTotalMB != int(p.Card.Memory.VRAM>>20)/m.Slices {
				t.Fatalf("%s entry %d: %+v", name, i, g)
			}
			uuids[g.UUID] = true
		}
	}
	if _, ok := LookupMode("TPX"); ok {
		t.Fatal("TPX does not exist on MI300X")
	}
	empty := &Fleet{}
	empty.SetMode(Modes[3])
	if gpus, _ := empty.All(); len(gpus) != 0 || len(empty.Trays()) != 0 {
		t.Fatal("an empty fleet grew entries")
	}
}

func TestConcurrentUse(t *testing.T) {
	f, _ := mi300x(t)
	sim := NewSimulator(f)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 200 {
			sim.Step()
		}
	}()
	for i := range 200 {
		f.Busy(i%8, 70, 50)
		f.All()
		f.Trays()
		if i%50 == 0 {
			f.SetMode(Modes[i/50%4])
		}
	}
	<-done
	if len(f.Catalog()) != 7 {
		t.Fatal("catalog changed under load")
	}
}
