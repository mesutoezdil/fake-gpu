package fleet

import (
	"math/rand"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func seeded(f *Fleet) *Simulator {
	s := NewSimulator(f)
	s.Rand = rand.New(rand.NewSource(1))
	return s
}

func steps(s *Simulator, n int) {
	for range n {
		s.Step()
	}
}

func TestTelemetryFollowsLoad(t *testing.T) {
	f, _ := mi300x(t)
	s := seeded(f)
	must(t)(f.Busy(0, 90, 50))
	steps(s, 200)
	busy, _ := f.Get(0)
	idle, _ := f.Get(1)
	if busy.PowerW < 2*idle.PowerW || busy.TempC < idle.TempC+10 {
		t.Fatalf("load does not drive telemetry: busy %d W %d C, idle %d W %d C", busy.PowerW, busy.TempC, idle.PowerW, idle.TempC)
	}
	if busy.Throttled || busy.GFXClockMHz < 1800 {
		t.Fatalf("busy GPU throttled without cause: %+v", busy)
	}

	must(t)(f.Throttle(0))
	steps(s, 200)
	throttled, _ := f.Get(0)
	if !throttled.Throttled || throttled.PowerW > busy.PowerCapW/2+5 || throttled.GFXClockMHz >= 1800 {
		t.Fatalf("throttling has no effect: %+v", throttled)
	}

	// Past the junction limit a GPU throttles itself.
	must(t)(f.Overheat(1, 105))
	steps(s, 50)
	if hot, _ := f.Get(1); !hot.Throttled || hot.TempC != 105 {
		t.Fatalf("overheating GPU not throttled: %+v", hot)
	}

	// A crashed GPU stays dark.
	must(t)(f.Crash(2))
	steps(s, 20)
	if dead, _ := f.Get(2); dead.PowerW != 0 || dead.Util != 0 || dead.GFXClockMHz != 0 {
		t.Fatalf("crashed GPU draws power: %+v", dead)
	}
}

func TestBusyHoldsRequestedLoad(t *testing.T) {
	f, _ := mi300x(t)
	s := seeded(f)
	must(t)(f.Busy(0, 95, 70))
	for i := range 2000 {
		s.Step()
		if g, _ := f.Get(0); g.Util < 92 || g.Util > 98 {
			t.Fatalf("tick %d: utilization %d left the 95%% target", i, g.Util)
		}
	}
	must(t)(f.Recover(0))
	s.Step()
	if g, _ := f.Get(0); g.TargetUtil != 0 || g.Util >= 8 {
		t.Fatalf("idle left load behind: %+v", g)
	}
}

func TestHIPClientMemoryCountsAsVRAM(t *testing.T) {
	f, _ := mi300x(t)
	root := t.TempDir()
	s := seeded(f)
	s.TrackHIP(root)
	s.Step()
	before, _ := f.Get(0)
	if err := os.MkdirAll(filepath.Join(root, "hip"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A live client holds a lock on its file; a dead one left it unlocked.
	live, err := os.OpenFile(filepath.Join(root, "hip/host-100"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(live.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	live.WriteString("0 68719476736\n2 1073741824\nnot a line\n-1 5\n")
	dead := filepath.Join(root, "hip/host-200")
	if err := os.WriteFile(dead, []byte("0 34359738368\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s.Step()
	g0, _ := f.Get(0)
	g2, _ := f.Get(2)
	if g0.MemUsedMB != before.MemUsedMB+65536 || g2.MemUsedMB != before.MemUsedMB+1024 {
		t.Fatalf("VRAM in use: GPU 0 %d MB, GPU 2 %d MB, baseline %d MB", g0.MemUsedMB, g2.MemUsedMB, before.MemUsedMB)
	}
	if _, err := os.Stat(dead); err == nil {
		t.Fatal("the file of a dead client was kept")
	}

	live.Close() // the client exits and its lock goes with it
	s.Step()
	if g0, _ = f.Get(0); g0.MemUsedMB != before.MemUsedMB {
		t.Fatalf("VRAM not released: %d MB, baseline %d MB", g0.MemUsedMB, before.MemUsedMB)
	}
}

func TestStartStop(t *testing.T) {
	f, _ := mi300x(t)
	s := NewSimulator(f)
	var ticks atomic.Int64
	s.OnTick = func(g *GPU) {
		if g.Index == 0 {
			ticks.Add(1)
		}
	}
	s.Start(time.Millisecond)
	s.Start(time.Millisecond) // a second start is a no-op
	deadline := time.Now().Add(5 * time.Second)
	for ticks.Load() < 3 {
		if time.Now().After(deadline) {
			t.Fatal("simulator does not tick")
		}
		time.Sleep(time.Millisecond)
	}
	s.Stop()
	s.Stop()
	stopped := ticks.Load()
	time.Sleep(20 * time.Millisecond)
	if ticks.Load() > stopped+1 {
		t.Fatal("simulator ticks after Stop")
	}
}
