package fleet

import "fmt"

// Tray is one server tray of the rack the console draws.
type Tray struct {
	Index   int    `json:"index"`
	First   int    `json:"start_gpu"`
	Last    int    `json:"end_gpu"`
	GPUs    int    `json:"gpu_count"`
	Profile string `json:"profile_slug"`
	Name    string `json:"profile_name"`
}

// TraySize is the GPUs per tray: an OAM baseboard holds eight, smaller
// fleets come as trays of four.
func TraySize(gpus int) int {
	if gpus >= 8 {
		return 8
	}
	return 4
}

// oam models sit on a fixed eight-GPU baseboard that cannot be split.
var oam = map[string]bool{
	"AMD Instinct MI300X": true,
	"AMD Instinct MI325X": true,
	"AMD Instinct MI350X": true,
	"AMD Instinct MI355X": true,
}

// Splittable reports whether trays of this model may run different profiles.
func Splittable(model string) bool { return !oam[model] }

// Trays lists the trays and the profile each runs.
func (f *Fleet) Trays() []Tray {
	f.mu.RLock()
	defer f.mu.RUnlock()
	size := TraySize(len(f.gpus))
	trays := make([]Tray, 0, (len(f.gpus)+size-1)/size)
	for first := 0; first < len(f.gpus); first += size {
		last := min(first+size, len(f.gpus)) - 1
		trays = append(trays, Tray{
			Index: first / size, First: first, Last: last, GPUs: last - first + 1,
			Profile: f.gpus[first].Profile, Name: f.gpus[first].Name,
		})
	}
	return trays
}

// SwitchTray moves the tray holding GPU i to another profile. Only the
// API state changes: the rendered tree keeps the node's startup profile.
func (f *Fleet) SwitchTray(i int, slug string) ([]GPU, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.profiles[slug]
	if i < 0 || i >= len(f.gpus) || !ok {
		return nil, false
	}
	size := TraySize(len(f.gpus))
	first := i / size * size
	last := min(first+size, len(f.gpus))
	c := p.Card
	for j := first; j < last; j++ {
		g := &f.gpus[j]
		g.Name, g.Architecture, g.Partition, g.Profile = c.Name, c.Architecture, c.Partition.Compute, slug
		g.PowerCapW, g.PowerW = int(c.Power.MaxCap), int(c.Power.Socket)
		g.MemTotalMB, g.MemUsedMB = int(c.Memory.VRAM/mib), int(c.Memory.VRAMUsed/mib)
		g.TempC, g.GFXClockMHz, g.MemClockMHz = int(c.Thermal.Edge), int(c.Clocks.GFX), int(c.Clocks.Mem)
		g.Status, g.ECCErrors, g.RetiredPages, g.Throttled = Healthy, 0, 0, false
		g.Util, g.TargetUtil = 0, 0
	}
	return append([]GPU(nil), f.gpus[first:last]...), true
}

// Mode is a compute partition mode of the console's virtual model.
type Mode struct {
	Name        string `json:"mode"`
	Slices      int    `json:"partitions"`
	Description string `json:"description"`
}

// Modes are the compute partition modes of MI300X-class GPUs. TPX does not
// exist on MI300X.
var Modes = []Mode{
	{"SPX", 1, "Single Partition: full GPU"},
	{"DPX", 2, "Dual Partition"},
	{"QPX", 4, "Quad Partition"},
	{"CPX", 8, "Compute Partition: one per XCD"},
}

// LookupMode finds a mode by name.
func LookupMode(name string) (Mode, bool) {
	for _, m := range Modes {
		if m.Name == name {
			return m, true
		}
	}
	return Mode{}, false
}

// SetMode shows every card as m.Slices entries. It is display state: the
// entries are derived from the running profile each time, so modes never
// compound, and nothing schedulable changes.
func (f *Fleet) SetMode(m Mode) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.gpus) == 0 {
		return
	}
	slug := f.gpus[0].Profile
	p, ok := f.profiles[slug]
	if !ok {
		return
	}
	cards := FromProfile(p, slug)
	n := max(m.Slices, 1)
	gpus := make([]GPU, 0, n*len(cards))
	for _, card := range cards {
		for s := range n {
			g := card
			g.Partition = m.Name
			if n > 1 {
				g.Index = len(gpus)
				g.UUID = fmt.Sprintf("%s-part%d", card.UUID, s)
				g.MemTotalMB, g.MemUsedMB = card.MemTotalMB/n, card.MemUsedMB/n
				g.PowerCapW, g.PowerW = card.PowerCapW/n, card.PowerW/n
			}
			gpus = append(gpus, g)
		}
	}
	f.gpus = gpus
}
