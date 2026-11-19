package fleet

import (
	"math"
	"math/rand"
	"sync"
	"time"
)

// Thermal model. Real cards differ by cooler and airflow; tune here.
const (
	ambientC       = 35.0 // inlet air
	fullLoadRiseC  = 55.0 // rise above ambient at the power cap
	idlePowerShare = 0.15 // share of the budget drawn at 0% utilization
	thermalAlpha   = 0.15 // share of the gap to steady state closed per tick
	throttleTempC  = 100  // junction limit that forces throttling
	throttleFactor = 0.5  // power budget and clock multiplier while throttled
	loadJitterPct  = 3    // utilization wobble around a held target
)

// Simulator derives telemetry from load once per tick: utilization sets
// power, power sets the temperature the GPU drifts toward, and throttling
// cuts both.
type Simulator struct {
	fleet *Fleet
	// Rand drives the noise; replace it before Start for a fixed sequence.
	Rand *rand.Rand
	// OnTick sees every GPU after each tick, under the fleet lock, so writes
	// it makes are ordered with the API's.
	OnTick func(*GPU)

	hipRoot string      // state directory of libfakehip clients; empty: off
	hipBase map[int]int // VRAM in use before a client appeared, by GPU index

	mu   sync.Mutex
	stop chan struct{}
}

// NewSimulator drives f.
func NewSimulator(f *Fleet) *Simulator {
	return &Simulator{fleet: f, Rand: rand.New(rand.NewSource(time.Now().UnixNano()))}
}

// TrackHIP counts device memory libfakehip clients hold under root as used VRAM.
func (s *Simulator) TrackHIP(root string) {
	s.hipRoot, s.hipBase = root, map[int]int{}
}

// Start ticks every interval until Stop. Starting twice is a no-op.
func (s *Simulator) Start(interval time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop != nil {
		return
	}
	s.stop = make(chan struct{})
	go func(stop <-chan struct{}) {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				s.Step()
			}
		}
	}(s.stop)
}

// Stop ends the ticks.
func (s *Simulator) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop != nil {
		close(s.stop)
		s.stop = nil
	}
}

// Step runs one tick.
func (s *Simulator) Step() {
	f := s.fleet
	f.mu.Lock()
	defer f.mu.Unlock()
	var hip map[int]uint64
	if s.hipRoot != "" {
		hip = ReadHIPUsage(s.hipRoot)
	}
	for i := range f.gpus {
		g := &f.gpus[i]
		if g.Status != Crashed {
			s.hold(g, hip)
			s.load(g)
			if g.PowerCapW > 0 {
				s.power(g)
			}
		}
	}
	if s.OnTick != nil {
		for i := range f.gpus {
			s.OnTick(&f.gpus[i])
		}
	}
}

// hold stacks memory a HIP client holds on what was in use before it came,
// and drops it again when the client goes.
func (s *Simulator) hold(g *GPU, hip map[int]uint64) {
	if s.hipBase == nil {
		return
	}
	base, tracked := s.hipBase[g.Index]
	held := int(hip[g.Index] >> 20)
	switch {
	case held > 0:
		if !tracked {
			base = g.MemUsedMB
			s.hipBase[g.Index] = base
		}
		g.MemUsedMB = min(base+held, g.MemTotalMB)
	case tracked:
		g.MemUsedMB = base
		delete(s.hipBase, g.Index)
	}
}

// load sets utilization, the input everything else follows: a held target
// with jitter, background noise on an idle healthy GPU, otherwise the value
// as written.
func (s *Simulator) load(g *GPU) {
	switch {
	case g.TargetUtil > 0:
		g.Util = clamp(g.TargetUtil+s.Rand.Intn(2*loadJitterPct+1)-loadJitterPct, 0, 100)
	case g.Util < 8 && g.Status == Healthy:
		g.Util = s.Rand.Intn(8)
	}
}

// power derives power, temperature and clock from utilization.
func (s *Simulator) power(g *GPU) {
	// A GPU throttles on request, or by itself at the junction limit.
	g.Throttled = g.Status == Throttling || g.TempC >= throttleTempC
	budget := float64(g.PowerCapW)
	if g.Throttled {
		budget *= throttleFactor
	}
	draw := budget*(idlePowerShare+(1-idlePowerShare)*float64(g.Util)/100) + float64(s.Rand.Intn(11)-5)
	g.PowerW = clamp(int(draw), min(50, g.PowerCapW), g.PowerCapW)

	// Temperature chases the steady state of the current draw, so a load
	// change shows up with thermal lag.
	if g.Status != Overheating {
		target := ambientC + fullLoadRiseC*float64(g.PowerW)/float64(g.PowerCapW)
		gap := target - float64(g.TempC)
		step := gap * thermalAlpha
		if math.Abs(step) < 1 && math.Abs(gap) >= 1 {
			step = math.Copysign(1, step)
		}
		g.TempC = clamp(int(math.Round(float64(g.TempC)+step))+s.Rand.Intn(3)-1, 30, 110)
	}

	clock := 400 + s.Rand.Intn(200)
	if g.Util > 20 {
		clock = 1800 + s.Rand.Intn(300)
	}
	if g.Throttled {
		clock = int(float64(clock) * throttleFactor)
	}
	g.GFXClockMHz = clock
}

func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }
