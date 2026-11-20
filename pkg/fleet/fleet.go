package fleet

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/mesutoezdil/fake-gpu/pkg/profile"
)

var (
	ErrNotFound = errors.New("gpu not found")
	ErrCrashed  = errors.New("gpu is crashed; recover it first")
)

// System is the node-level part of the API.
type System struct {
	Driver  string `json:"amdgpu_version"`
	ROCm    string `json:"rocm_version"`
	AMDSMI  string `json:"amdsmi_version"`
	Profile string `json:"profile_name"`
	Node    string `json:"node_name"`
}

// Summary is one entry of the profile selector.
type Summary struct {
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	Architecture string `json:"architecture"`
	MemoryGB     int    `json:"memory_gb"`
	GPUs         int    `json:"gpu_count"`
	TDPW         int    `json:"tdp_w"`
}

// Fleet is every GPU of the node. All methods are safe for concurrent use
// and hand out copies.
type Fleet struct {
	mu       sync.RWMutex
	gpus     []GPU
	sys      System
	profiles map[string]*profile.Profile
	catalog  []Summary
}

// New boots a fleet from the running profile, known under slug.
func New(p *profile.Profile, slug string) *Fleet {
	return &Fleet{
		gpus: FromProfile(p, slug),
		sys: System{
			Driver:  p.Software.Driver,
			ROCm:    p.Software.ROCm,
			AMDSMI:  p.Software.AMDSMI,
			Profile: p.Card.Name,
			Node:    "mock-node",
		},
		profiles: map[string]*profile.Profile{slug: p},
		catalog:  []Summary{},
	}
}

// AddCatalog makes the profiles of c available for switching. The running
// profile keeps its slug: its startup partitioning stays what recovery and
// virtual partitions start from.
func (f *Fleet) AddCatalog(c *profile.Catalog) {
	f.mu.Lock()
	defer f.mu.Unlock()
	running := ""
	if len(f.gpus) > 0 {
		running = f.gpus[0].Profile
	}
	for _, slug := range c.Slugs {
		p := c.Profiles[slug]
		if slug != running {
			f.profiles[slug] = p
		}
		f.catalog = append(f.catalog, Summary{
			Slug:         slug,
			Name:         p.Card.Name,
			Architecture: p.Card.Architecture,
			MemoryGB:     int(p.Card.Memory.VRAM >> 30),
			GPUs:         len(p.Devices),
			TDPW:         int(p.Card.Power.MaxCap),
		})
	}
}

// Catalog lists the switchable profiles.
func (f *Fleet) Catalog() []Summary {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return append([]Summary(nil), f.catalog...)
}

// All returns every GPU and the node state.
func (f *Fleet) All() ([]GPU, System) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return append([]GPU(nil), f.gpus...), f.sys
}

// Get returns one GPU.
func (f *Fleet) Get(i int) (GPU, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if i < 0 || i >= len(f.gpus) {
		return GPU{}, false
	}
	return f.gpus[i], true
}

// update applies change to GPU i under the lock and returns the result.
func (f *Fleet) update(i int, change func(*GPU) error) (GPU, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < 0 || i >= len(f.gpus) {
		return GPU{}, ErrNotFound
	}
	g := f.gpus[i]
	if err := change(&g); err != nil {
		return f.gpus[i], err
	}
	f.gpus[i] = g
	return g, nil
}

// Patch overwrites the fields a JSON object names. A value of the wrong
// type rejects the whole patch.
func (f *Fleet) Patch(i int, patch json.RawMessage) (GPU, error) {
	return f.update(i, func(g *GPU) error {
		if err := json.Unmarshal(patch, g); err != nil {
			return fmt.Errorf("invalid patch: %w", err)
		}
		return nil
	})
}

// idle is the state recovery returns a GPU to: its profile's defaults, or
// MI300X values for a GPU whose profile is unknown.
func (f *Fleet) idle(g *GPU) {
	temp, power, gfx, mem, used := 49, 183, 500, 1300, 300
	if p, ok := f.profiles[g.Profile]; ok {
		c := p.Card
		temp, power = int(c.Thermal.Edge), int(c.Power.Socket)
		gfx, mem = int(c.Clocks.GFX), int(c.Clocks.Mem)
		used = int(c.Memory.VRAMUsed / mib)
	}
	g.Status, g.Util, g.TargetUtil = Healthy, 0, 0
	g.TempC, g.PowerW, g.GFXClockMHz, g.MemClockMHz, g.MemUsedMB = temp, power, gfx, mem, used
	g.ECCErrors, g.RetiredPages, g.Throttled = 0, 0, false
}

// Recover clears any fault and load.
func (f *Fleet) Recover(i int) (GPU, error) {
	return f.update(i, func(g *GPU) error { f.idle(g); return nil })
}

// toggle sets a fault, or recovers the GPU when it is already in it.
func (f *Fleet) toggle(i int, s Status, set func(*GPU)) (GPU, error) {
	return f.update(i, func(g *GPU) error {
		if g.Status == s {
			f.idle(g)
		} else {
			g.Status = s
			set(g)
		}
		return nil
	})
}

// toggleLive is toggle for faults a crashed GPU cannot take.
func (f *Fleet) toggleLive(i int, s Status, set func(*GPU)) (GPU, error) {
	return f.update(i, func(g *GPU) error {
		switch g.Status {
		case Crashed:
			return ErrCrashed
		case s:
			f.idle(g)
		default:
			g.Status = s
			set(g)
		}
		return nil
	})
}

// Crash takes the GPU off the bus.
func (f *Fleet) Crash(i int) (GPU, error) {
	return f.toggle(i, Crashed, func(g *GPU) {
		g.Util, g.TargetUtil, g.PowerW, g.GFXClockMHz, g.MemClockMHz, g.TempC = 0, 0, 0, 0, 0, 0
	})
}

// Busy holds util percent of load with mem percent of VRAM in use. A GPU
// already above 50% goes idle instead.
func (f *Fleet) Busy(i, util, mem int) (GPU, error) {
	return f.update(i, func(g *GPU) error {
		if g.Util > 50 {
			f.idle(g)
			return nil
		}
		g.Status, g.Util, g.TargetUtil = Healthy, util, util
		g.MemUsedMB = g.MemTotalMB * mem / 100
		g.PowerW = g.PowerCapW * util / 100
		g.GFXClockMHz = 2100
		return nil
	})
}

// Overheat pins the temperature.
func (f *Fleet) Overheat(i, tempC int) (GPU, error) {
	return f.toggle(i, Overheating, func(g *GPU) { g.TempC = tempC })
}

// ECC adds uncorrectable errors.
func (f *Fleet) ECC(i, count int) (GPU, error) {
	return f.toggle(i, ECCError, func(g *GPU) { g.ECCErrors += count })
}

// Throttle halves the power budget; the simulator keeps it down.
func (f *Fleet) Throttle(i int) (GPU, error) {
	return f.toggleLive(i, Throttling, func(g *GPU) { g.Throttled = true })
}

// RetirePages takes VRAM pages out of service. It is the handled outcome of
// a memory error, so the GPU keeps running.
func (f *Fleet) RetirePages(i, count int) (GPU, error) {
	return f.toggleLive(i, PageRetired, func(g *GPU) { g.RetiredPages += count })
}

// XGMIDown takes the GPU's xGMI links down.
func (f *Fleet) XGMIDown(i int) (GPU, error) {
	return f.toggleLive(i, XGMIDown, func(*GPU) {})
}

// Profile returns a known profile.
func (f *Fleet) Profile(slug string) (*profile.Profile, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	p, ok := f.profiles[slug]
	return p, ok
}

// Switch boots every GPU again from another profile.
func (f *Fleet) Switch(slug string) ([]GPU, *profile.Profile, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.profiles[slug]
	if !ok {
		return nil, nil, false
	}
	f.gpus = FromProfile(p, slug)
	f.sys.Profile, f.sys.Driver, f.sys.ROCm = p.Card.Name, p.Software.Driver, p.Software.ROCm
	return append([]GPU(nil), f.gpus...), p, true
}
