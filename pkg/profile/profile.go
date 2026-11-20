// Package profile describes one AMD GPU model and the cards of one node: the
// YAML schema under profiles/, its loader, and the fixed startup partitioning.
//
// Everything the mock renders derives from a Profile, so the types here hold
// the values the amdgpu driver would expose and nothing about runtime state.
package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Profile is one YAML file under profiles/.
type Profile struct {
	Version  string   `yaml:"version"`
	Software Software `yaml:"system"`
	Card     Card     `yaml:"device_defaults"`
	XGMI     XGMI     `yaml:"xgmi"`
	Topology Topology `yaml:"pcie_topology"`
	Devices  []Device `yaml:"devices"`
}

// Software is the driver stack the node reports.
type Software struct {
	Driver string `yaml:"amdgpu_version"`
	ROCm   string `yaml:"rocm_version"`
	AMDSMI string `yaml:"amdsmi_version"`
}

// Card holds what every card of the model shares.
type Card struct {
	Name string `yaml:"name"`
	// Product is the raw /sys/class/drm/cardN/device/product_name, which
	// AMD's DRA driver publishes as productName. Empty means Name.
	Product         string `yaml:"product_name,omitempty"`
	Vendor          uint32 `yaml:"vendor_id"`
	Device          uint32 `yaml:"device_id"`
	SubsystemVendor uint32 `yaml:"subsystem_vendor_id"`
	SubsystemDevice uint32 `yaml:"subsystem_device_id"`
	GFXTarget       uint32 `yaml:"gfx_target_version"`
	Architecture    string `yaml:"architecture"`

	Compute     Compute     `yaml:"compute"`
	Memory      Memory      `yaml:"memory"`
	Power       Power       `yaml:"power"`
	Thermal     Thermal     `yaml:"thermal"`
	Clocks      Clocks      `yaml:"clocks"`
	PCIe        PCIe        `yaml:"pcie"`
	ECC         ECC         `yaml:"ecc"`
	Firmware    Firmware    `yaml:"firmware"`
	Utilization Utilization `yaml:"utilization"`
	Partition   Modes       `yaml:"partition"`
}

// ProductName is the value of the product_name sysfs attribute.
func (c *Card) ProductName() string {
	if c.Product != "" {
		return c.Product
	}
	return c.Name
}

// Compute mirrors the KFD topology node properties of the same names.
type Compute struct {
	CUs              uint32 `yaml:"cu_count"`
	SIMDPerCU        uint32 `yaml:"simd_per_cu"`
	SIMDs            uint32 `yaml:"simd_count"`
	StreamProcessors uint32 `yaml:"stream_processors"`
	Wavefront        uint32 `yaml:"wave_front_size"`
	Arrays           uint32 `yaml:"array_count"`
	ArraysPerEngine  uint32 `yaml:"simd_arrays_per_engine"`
	CUsPerArray      uint32 `yaml:"cu_per_simd_array"`
	XCCs             uint32 `yaml:"num_xcc"`
	WavesPerSIMD     uint32 `yaml:"max_waves_per_simd"`
	LDSKiB           uint32 `yaml:"lds_size_in_kb"`
	GDSKiB           uint32 `yaml:"gds_size_in_kb"`
	SDMAEngines      uint32 `yaml:"num_sdma_engines"`
	SDMAXGMIEngines  uint32 `yaml:"num_sdma_xgmi_engines"`
	CPQueues         uint32 `yaml:"num_cp_queues"`
}

type Memory struct {
	Type        string `yaml:"vram_type"`
	VRAM        uint64 `yaml:"vram_size_bytes"`
	VRAMUsed    uint64 `yaml:"vram_used_bytes"`
	BusWidth    uint32 `yaml:"vram_bus_width"`
	VisibleVRAM uint64 `yaml:"vis_vram_size_bytes"`
	GTT         uint64 `yaml:"gtt_size_bytes"`
	MaxClockMHz uint32 `yaml:"max_mem_clk_mhz"`
}

// Power is in watts.
type Power struct {
	Socket     uint32 `yaml:"current_socket_power_w"`
	DefaultCap uint32 `yaml:"default_power_cap_w"`
	MaxCap     uint32 `yaml:"max_power_cap_w"`
	MinCap     uint32 `yaml:"min_power_cap_w"`
}

// Thermal is in degrees Celsius.
type Thermal struct {
	Edge     uint32 `yaml:"edge_temperature_c"`
	Hotspot  uint32 `yaml:"hotspot_temperature_c"`
	Mem      uint32 `yaml:"mem_temperature_c"`
	Shutdown uint32 `yaml:"shutdown_temperature_c"`
	Slowdown uint32 `yaml:"slowdown_temperature_c"`
}

// Clocks is in MHz.
type Clocks struct {
	GFXMax uint32 `yaml:"max_gfx_clk_mhz"`
	GFXMin uint32 `yaml:"min_gfx_clk_mhz"`
	GFX    uint32 `yaml:"current_gfx_clk_mhz"`
	MemMax uint32 `yaml:"max_mem_clk_mhz"`
	MemMin uint32 `yaml:"min_mem_clk_mhz"`
	Mem    uint32 `yaml:"current_mem_clk_mhz"`
}

type PCIe struct {
	Gen      uint32 `yaml:"gen"`
	Width    uint32 `yaml:"width"`
	MaxGen   uint32 `yaml:"max_gen"`
	MaxWidth uint32 `yaml:"max_width"`
	Replays  uint32 `yaml:"replay_count"`
}

type ECC struct {
	Enabled       bool   `yaml:"enabled"`
	Correctable   uint64 `yaml:"correctable_count"`
	Uncorrectable uint64 `yaml:"uncorrectable_count"`
}

type Firmware struct {
	PMFW string `yaml:"pmfw_version"`
}

// Utilization is in percent.
type Utilization struct {
	GFX uint32 `yaml:"gfx_activity_percent"`
	UMC uint32 `yaml:"umc_activity_percent"`
	MM  uint32 `yaml:"mm_activity_percent"`
}

// Modes are the compute (SPX, DPX, ...) and memory (NPS1, NPS2) partition modes.
type Modes struct {
	Compute string `yaml:"mode"`
	Memory  string `yaml:"nps_mode"`
}

type XGMI struct {
	Hive         string `yaml:"hive_id"`
	LinksPerGPU  uint32 `yaml:"links_per_gpu"`
	LanesPerLink uint32 `yaml:"lanes_per_link"`
	LinkGBps     uint32 `yaml:"bandwidth_per_link_gbps"`
	LinkType     uint32 `yaml:"link_type"`
}

type Topology struct {
	RootComplexes []RootComplex `yaml:"root_complexes"`
}

// RootComplex groups the cards behind one PCIe root, for example pci0000:00.
type RootComplex struct {
	ID      string   `yaml:"id"`
	NUMA    int      `yaml:"numa_node"`
	Devices []string `yaml:"devices"`
}

// Device is one card, or one compute partition of a card.
type Device struct {
	Slice       int    `yaml:"partition_index"`
	Index       int    `yaml:"index"`
	UUID        string `yaml:"uuid"`
	BDF         string `yaml:"pci_bdf"`
	RenderMinor int    `yaml:"drm_render_minor"`
	UniqueID    string `yaml:"unique_id"`
	XGMIID      string `yaml:"xgmi_device_id"`
}

// Load reads one profile file.
func Load(path string) (*Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Profile
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &p, nil
}

// Catalog is every profile of a directory, keyed by file name without .yaml.
type Catalog struct {
	Slugs    []string // sorted
	Profiles map[string]*Profile
}

// LoadDir reads every *.yaml file in dir. Files that do not parse are left
// out, so one broken profile does not take the others down.
func LoadDir(dir string) (*Catalog, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading profile dir: %w", err)
	}
	c := &Catalog{Profiles: map[string]*Profile{}}
	for _, e := range entries {
		slug, ok := strings.CutSuffix(e.Name(), ".yaml")
		if e.IsDir() || !ok {
			continue
		}
		if p, err := Load(filepath.Join(dir, e.Name())); err == nil {
			c.Slugs = append(c.Slugs, slug)
			c.Profiles[slug] = p
		}
	}
	sort.Strings(c.Slugs)
	return c, nil
}

// Root returns the root complex holding a PCI address.
func (p *Profile) Root(bdf string) (RootComplex, bool) {
	for _, rc := range p.Topology.RootComplexes {
		for _, d := range rc.Devices {
			if d == bdf {
				return rc, true
			}
		}
	}
	return RootComplex{}, false
}

// NUMA returns the NUMA node of a PCI address, -1 when the topology lacks it.
func (p *Profile) NUMA(bdf string) int {
	if rc, ok := p.Root(bdf); ok {
		return rc.NUMA
	}
	return -1
}

// Device returns the device at a card index.
func (p *Profile) Device(index int) (Device, bool) {
	for _, d := range p.Devices {
		if d.Index == index {
			return d, true
		}
	}
	return Device{}, false
}

// RenderMinor is the DRM render minor of a card. Profiles state it; 128+index
// is only the layout of cards probed in order with nothing else on the bus.
func (p *Profile) RenderMinor(index int) int {
	if d, ok := p.Device(index); ok {
		return d.RenderMinor
	}
	return 128 + index
}

// ParseID reads an ID written as decimal or 0x-prefixed hex; empty is 0.
func ParseID(s string) (uint64, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.ParseUint(s, 0, 64)
}

// BDF is a parsed PCI address, domain:bus:device.function.
type BDF struct{ Domain, Bus, Dev, Fn uint32 }

// ParseBDF reads "DDDD:BB:DD.F". Fields that do not parse stay zero, as KFD
// location IDs of a malformed address would.
func ParseBDF(s string) BDF {
	var b BDF
	fmt.Sscanf(s, "%x:%x:%x.%x", &b.Domain, &b.Bus, &b.Dev, &b.Fn)
	return b
}

// Location is the KFD location_id: bus, device and function packed.
func (b BDF) Location() uint32 { return b.Bus<<8 | b.Dev<<3 | b.Fn }
