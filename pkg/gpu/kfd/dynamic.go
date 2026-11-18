package kfd

import (
	"math"
	"math/rand"
	"sync"
	"time"
)

// DynamicSimulator derives telemetry from load: utilization sets power,
// power sets the temperature the GPU drifts toward, and throttling cuts both.
type DynamicSimulator struct {
	mu      sync.Mutex
	running bool
	stop    chan struct{}
	state   *FleetState
	rng     *rand.Rand
	onTick  func(*GPUState)
}

func NewDynamicSimulator(state *FleetState, callbacks ...func(*GPUState)) *DynamicSimulator {
	var callback func(*GPUState)
	if len(callbacks) > 0 {
		callback = callbacks[0]
	}
	return &DynamicSimulator{
		onTick: callback,
		state:  state,
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (d *DynamicSimulator) Start(intervalMs int) {
	d.mu.Lock()
	if d.running {
		d.mu.Unlock()
		return
	}
	d.running = true
	d.stop = make(chan struct{})
	d.mu.Unlock()

	go d.loop(time.Duration(intervalMs) * time.Millisecond)
}

func (d *DynamicSimulator) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running {
		close(d.stop)
		d.running = false
	}
}

func (d *DynamicSimulator) loop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-d.stop:
			return
		case <-ticker.C:
			d.tick()
		}
	}
}

func (d *DynamicSimulator) tick() {
	d.state.mu.Lock()
	defer d.state.mu.Unlock()
	defer func() {
		if d.onTick != nil {
			for i := range d.state.GPUs {
				d.onTick(&d.state.GPUs[i])
			}
		}
	}()

	for i := range d.state.GPUs {
		gpu := &d.state.GPUs[i]

		if gpu.Status == "crashed" {
			continue
		}

		// Utilization is the workload input. The busy action sets a target
		// that is held with jitter; a PATCHed value stays as written; an idle
		// GPU shows background noise. Everything below derives from it.
		switch {
		case gpu.TargetUtilPct > 0:
			gpu.UtilizationPct = clampInt(gpu.TargetUtilPct+d.rng.Intn(2*loadJitterPct+1)-loadJitterPct, 0, 100)
		case gpu.UtilizationPct < 8 && gpu.Status == "healthy":
			gpu.UtilizationPct = d.rng.Intn(8)
		}
		if gpu.PowerCapW <= 0 {
			continue
		}

		// A GPU throttles on request or by itself at the junction limit.
		gpu.Throttled = gpu.Status == "throttling" || gpu.TemperatureC >= throttleTempC
		budget := float64(gpu.PowerCapW)
		if gpu.Throttled {
			budget *= throttleFactor
		}
		power := budget*(idlePowerShare+(1-idlePowerShare)*float64(gpu.UtilizationPct)/100) + float64(d.rng.Intn(11)-5)
		gpu.PowerW = clampInt(int(power), min(50, gpu.PowerCapW), gpu.PowerCapW)

		// Temperature chases the steady state for the current power draw, so
		// load changes show up with thermal lag instead of instantly.
		if gpu.Status != "overheating" {
			target := ambientC + fullLoadRiseC*float64(gpu.PowerW)/float64(gpu.PowerCapW)
			step := (target - float64(gpu.TemperatureC)) * thermalAlpha
			if math.Abs(step) < 1 && math.Abs(target-float64(gpu.TemperatureC)) >= 1 {
				step = math.Copysign(1, step)
			}
			gpu.TemperatureC = clampInt(int(math.Round(float64(gpu.TemperatureC)+step))+d.rng.Intn(3)-1, 30, 110)
		}

		clock := 400 + d.rng.Intn(200)
		if gpu.UtilizationPct > 20 {
			clock = 1800 + d.rng.Intn(300)
		}
		if gpu.Throttled {
			clock = int(float64(clock) * throttleFactor)
		}
		gpu.ClockGFXMHz = clock
	}
}

// Thermal model knobs. Real cards differ by cooler and airflow; tune here.
const (
	ambientC       = 35.0 // inlet air
	fullLoadRiseC  = 55.0 // rise above ambient at the power cap
	idlePowerShare = 0.15 // share of the budget drawn at 0% utilization
	thermalAlpha   = 0.15 // share of the gap to steady state closed per tick
	throttleTempC  = 100  // junction limit that forces throttling
	throttleFactor = 0.5  // power budget and clock multiplier while throttled
	loadJitterPct  = 3    // utilization wobble around a held target
)

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
