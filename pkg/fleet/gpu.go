// Package fleet is the runtime state of a node's mock GPUs: what the API
// serves and changes, what the simulator drives, and what the renderers
// write out. It knows nothing about files or HTTP.
package fleet

import "github.com/mesutoezdil/fake-gpu/pkg/profile"

// Status is the fault a GPU is in; only one at a time.
type Status string

const (
	Healthy     Status = "healthy"
	Crashed     Status = "crashed"
	Overheating Status = "overheating"
	ECCError    Status = "ecc_error"
	Throttling  Status = "throttling"
	PageRetired Status = "page_retired"
	XGMIDown    Status = "xgmi_link_down"
)

// GPU is one card, or one partition of a card, as the API reports it. The
// JSON names are the API contract.
type GPU struct {
	Slice           int    `json:"partition_index"`
	MemoryPartition string `json:"memory_partition"`
	Index           int    `json:"index"`
	UUID            string `json:"uuid"`
	Name            string `json:"name"`
	BDF             string `json:"pci_bdf"`
	NUMA            int    `json:"numa_node"`
	Architecture    string `json:"architecture"`
	Status          Status `json:"status"`
	TempC           int    `json:"temperature_c"`
	PowerW          int    `json:"power_w"`
	PowerCapW       int    `json:"power_cap_w"`
	Util            int    `json:"utilization_pct"`
	TargetUtil      int    `json:"target_utilization_pct"` // 0: no sustained load
	MemUsedMB       int    `json:"memory_used_mb"`
	MemTotalMB      int    `json:"memory_total_mb"`
	ECCErrors       int    `json:"ecc_errors"`
	RetiredPages    int    `json:"retired_pages"`
	Throttled       bool   `json:"throttled"`
	GFXClockMHz     int    `json:"clock_gfx_mhz"`
	MemClockMHz     int    `json:"clock_mem_mhz"`
	Partition       string `json:"partition"`
	Profile         string `json:"profile_slug"`
}

const mib = 1 << 20

// FromProfile returns the GPUs a profile boots with, all healthy and idle.
func FromProfile(p *profile.Profile, slug string) []GPU {
	c := p.Card
	gpus := make([]GPU, len(p.Devices))
	for i, d := range p.Devices {
		gpus[i] = GPU{
			Index:           d.Index,
			Slice:           d.Slice,
			MemoryPartition: c.Partition.Memory,
			UUID:            d.UUID,
			Name:            c.Name,
			BDF:             d.BDF,
			NUMA:            p.NUMA(d.BDF),
			Architecture:    c.Architecture,
			Status:          Healthy,
			TempC:           int(c.Thermal.Edge),
			PowerW:          int(c.Power.Socket),
			PowerCapW:       int(c.Power.MaxCap),
			Util:            int(c.Utilization.GFX),
			MemUsedMB:       int(c.Memory.VRAMUsed / mib),
			MemTotalMB:      int(c.Memory.VRAM / mib),
			GFXClockMHz:     int(c.Clocks.GFX),
			MemClockMHz:     int(c.Clocks.Mem),
			Partition:       c.Partition.Compute,
			Profile:         slug,
		}
	}
	return gpus
}
