// Package capture builds a profile from the amdgpu sysfs tree of a real
// machine, so the mock can stand in for hardware it has never seen.
package capture

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/mesutoezdil/fake-gpu/pkg/profile"
)

// Options steer Capture.
type Options struct {
	BDF          string // reference GPU; empty picks the one with the most VRAM
	Name         string // marketing name; empty falls back to sysfs product_name
	Architecture string
	KeepIDs      bool // keep the cards' real unique IDs instead of placeholders
}

const (
	amd      = 0x1002
	xgmiType = 11
	uW, mC   = 1_000_000, 1000
)

// gpu is one KFD GPU node and the sysfs directories describing it.
type gpu struct {
	props, bank map[string]uint64
	bdf         string
	xgmi        uint32
	dirs        []string // PCI device, DRM cards, their hwmon devices
}

// attr is the trimmed content of the first readable attribute. A runtime
// suspended GPU answers EBUSY for its live sensors, hence "readable".
func (g *gpu) attr(name string) (string, bool) {
	for _, dir := range g.dirs {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			return strings.TrimSpace(string(b)), true
		}
	}
	return "", false
}

// num is the leading number of an attribute, decimal or 0x hex.
func (g *gpu) num(name string) (uint64, bool) {
	s, ok := g.attr(name)
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.Fields(s + " x")[0], 0, 64)
	return v, err == nil
}

// scaled is num divided by unit, and whether the attribute was there.
func (g *gpu) scaled(name string, unit uint64) (uint32, bool) {
	v, ok := g.num(name)
	return uint32(v / unit), ok
}

// vram prefers dedicated VRAM: KFD's memory bank of an integrated GPU
// counts shared system memory and would outrank a discrete card.
func (g *gpu) vram() uint64 {
	if v, ok := g.num("mem_info_vram_total"); ok {
		return v
	}
	return g.bank["size_in_bytes"]
}

// scan reads every AMD GPU node of the KFD topology under sys.
func scan(sys string) []*gpu {
	nodes, _ := filepath.Glob(filepath.Join(sys, "class/kfd/kfd/topology/nodes/*"))
	sort.Slice(nodes, func(a, b int) bool { return nodeNumber(nodes[a]) < nodeNumber(nodes[b]) })
	cards, _ := filepath.Glob(filepath.Join(sys, "class/drm/card*/device"))
	var gpus []*gpu
	for _, dir := range nodes {
		props := readProps(filepath.Join(dir, "properties"))
		if props["vendor_id"] != amd || props["simd_count"] == 0 {
			continue // the CPU, or a node that cannot be read
		}
		loc := props["location_id"]
		g := &gpu{
			props: props,
			bank:  readProps(filepath.Join(dir, "mem_banks/0/properties")),
			bdf:   fmt.Sprintf("%04x:%02x:%02x.%x", props["domain"], loc>>8&0xff, loc>>3&0x1f, loc&7),
		}
		links, _ := filepath.Glob(filepath.Join(dir, "io_links/*/properties"))
		for _, link := range links {
			if readProps(link)["type"] == xgmiType {
				g.xgmi++
			}
		}
		g.dirs = []string{filepath.Join(sys, "bus/pci/devices", g.bdf)}
		for _, c := range cards {
			if b, err := os.ReadFile(filepath.Join(c, "uevent")); err == nil && strings.Contains(string(b), "PCI_SLOT_NAME="+g.bdf) {
				g.dirs = append(g.dirs, c)
			}
		}
		for _, d := range append([]string(nil), g.dirs...) {
			hwmon, _ := filepath.Glob(filepath.Join(d, "hwmon/hwmon*"))
			g.dirs = append(g.dirs, hwmon...)
		}
		gpus = append(gpus, g)
	}
	return gpus
}

func nodeNumber(dir string) int {
	n, _ := strconv.Atoi(filepath.Base(dir))
	return n
}

// Capture builds a profile from the sysfs tree under root ("/" on a real
// machine). A profile describes one model, so cards of another model than
// the reference GPU are left out; notes say so, and name every value that
// could not be read.
func Capture(root string, o Options) (*profile.Profile, []string, error) {
	sys := filepath.Join(root, "sys")
	all := scan(sys)
	if len(all) == 0 {
		return nil, nil, fmt.Errorf("no AMD GPU in %s/class/kfd/kfd/topology; is the amdgpu driver loaded?", sys)
	}
	ref := all[0]
	for _, g := range all {
		if o.BDF == g.bdf || (o.BDF == "" && g.vram() > ref.vram()) {
			ref = g
		}
	}
	if o.BDF != "" && ref.bdf != o.BDF {
		return nil, nil, fmt.Errorf("no AMD GPU at %s", o.BDF)
	}

	c := &capturer{ref: ref}
	var gpus []*gpu
	for _, g := range all {
		if g.props["device_id"] == ref.props["device_id"] {
			gpus = append(gpus, g)
		} else {
			c.notef("left out %s (device 0x%04x): another model, capture it with --gpu %s", g.bdf, g.props["device_id"], g.bdf)
		}
	}

	p := &profile.Profile{Version: "1.0"}
	if v, err := os.ReadFile(filepath.Join(sys, "module/amdgpu/version")); err == nil {
		p.Software.Driver = strings.TrimSpace(string(v))
	} else if v, err := os.ReadFile(filepath.Join(root, "proc/sys/kernel/osrelease")); err == nil {
		p.Software.Driver = strings.TrimSpace(string(v)) // in-tree driver
	}
	c.card(&p.Card, o)
	if hive := ref.props["hive_id"]; hive != 0 || ref.xgmi > 0 {
		p.XGMI = profile.XGMI{LinksPerGPU: ref.xgmi, LinkType: xgmiType}
		if hive != 0 {
			p.XGMI.Hive = fmt.Sprintf("0x%016x", hive)
		}
	}
	p.Devices, p.Topology = devices(gpus, p.Card.Device, o.KeepIDs)
	return p, c.notes, nil
}

// capturer reads the reference GPU and records what it could not.
type capturer struct {
	ref   *gpu
	notes []string
}

func (c *capturer) notef(format string, args ...any) {
	c.notes = append(c.notes, fmt.Sprintf(format, args...))
}

// need reads an attribute the profile cannot do without.
func (c *capturer) need(what, name string, unit uint64) uint32 {
	v, ok := c.ref.scaled(name, unit)
	if !ok {
		c.notef("%s unknown: %s not readable", what, name)
	}
	return v
}

// either reads the first attribute that is there.
func (c *capturer) either(unit uint64, names ...string) uint32 {
	for _, n := range names {
		if v, ok := c.ref.scaled(n, unit); ok {
			return v
		}
	}
	return 0
}

func (c *capturer) card(d *profile.Card, o Options) {
	g, k := c.ref, c.ref.props
	d.Product, _ = g.attr("product_name")
	d.Name = o.Name
	if d.Name == "" {
		d.Name = d.Product
	}
	if d.Name == "" {
		d.Name = fmt.Sprintf("AMD GPU 1002:%04x", k["device_id"])
		c.notef("name is a placeholder: no product_name in sysfs, pass --name")
	}
	d.Architecture = o.Architecture
	if d.Architecture == "" {
		d.Architecture = "unknown"
	}
	d.Vendor, d.Device = amd, uint32(k["device_id"])
	d.SubsystemVendor = c.need("subsystem vendor", "subsystem_vendor", 1)
	d.SubsystemDevice = c.need("subsystem device", "subsystem_device", 1)
	d.GFXTarget = uint32(k["gfx_target_version"])

	d.Compute = profile.Compute{
		SIMDPerCU: uint32(k["simd_per_cu"]), SIMDs: uint32(k["simd_count"]),
		Wavefront: uint32(k["wave_front_size"]), Arrays: uint32(k["array_count"]),
		ArraysPerEngine: uint32(k["simd_arrays_per_engine"]), CUsPerArray: uint32(k["cu_per_simd_array"]),
		XCCs: uint32(k["num_xcc"]), WavesPerSIMD: uint32(k["max_waves_per_simd"]),
		LDSKiB: uint32(k["lds_size_in_kb"]), GDSKiB: uint32(k["gds_size_in_kb"]),
		SDMAEngines: uint32(k["num_sdma_engines"]), SDMAXGMIEngines: uint32(k["num_sdma_xgmi_engines"]),
		CPQueues: uint32(k["num_cp_queues"]),
	}
	if d.Compute.SIMDPerCU > 0 {
		d.Compute.CUs = d.Compute.SIMDs / d.Compute.SIMDPerCU
		d.Compute.StreamProcessors = d.Compute.CUs * d.Compute.Wavefront
	}

	m := &d.Memory
	m.Type, _ = g.attr("mem_info_vram_vendor")
	m.VRAM, m.BusWidth, m.MaxClockMHz = g.bank["size_in_bytes"], uint32(g.bank["width"]), uint32(g.bank["mem_clk_max"])
	if v, ok := g.num("mem_info_vram_total"); ok {
		m.VRAM = v
	}
	m.VRAMUsed, _ = g.num("mem_info_vram_used")
	if m.VisibleVRAM, _ = g.num("mem_info_vis_vram_total"); m.VisibleVRAM == 0 {
		m.VisibleVRAM = m.VRAM
	}
	m.GTT, _ = g.num("mem_info_gtt_total")

	d.Power.MaxCap = c.need("power cap", "power1_cap_max", uW)
	if d.Power.MaxCap == 0 {
		c.notef("max_power_cap_w is 0, so the mock cannot derive power or temperature: set it by hand. A suspended GPU hides its sensors until it is active again; an integrated GPU has no power cap at all")
	}
	d.Power.DefaultCap = c.either(uW, "power1_cap_default", "power1_cap")
	d.Power.MinCap = c.either(uW, "power1_cap_min")
	d.Power.Socket = c.either(uW, "power1_average", "power1_input")

	d.Thermal.Edge = c.need("edge temperature", "temp1_input", mC)
	d.Thermal.Hotspot = c.either(mC, "temp2_input")
	d.Thermal.Mem = c.either(mC, "temp3_input")
	d.Thermal.Slowdown = c.either(mC, "temp1_crit")
	d.Thermal.Shutdown = c.either(mC, "temp1_emergency")

	clk := &d.Clocks
	var ok bool
	if clk.GFXMin, clk.GFXMax, clk.GFX, ok = levels(g, "pp_dpm_sclk"); !ok {
		c.notef("GFX clock range unknown: pp_dpm_sclk not readable")
	}
	clk.MemMin, clk.MemMax, clk.Mem, _ = levels(g, "pp_dpm_mclk")
	if clk.MemMax == 0 {
		clk.MemMax, clk.Mem = m.MaxClockMHz, m.MaxClockMHz
	}

	d.PCIe.Gen = linkGen(g, "current_link_speed")
	d.PCIe.Width = c.need("PCIe width", "current_link_width", 1)
	d.PCIe.MaxGen = linkGen(g, "max_link_speed")
	d.PCIe.MaxWidth = c.either(1, "max_link_width")

	if _, err := os.Stat(filepath.Join(g.dirs[0], "ras")); err == nil {
		d.ECC.Enabled = true
	}
	d.Firmware.PMFW, _ = g.attr("fw_version/smc_fw_version")
	d.Partition = profile.Modes{Compute: "SPX", Memory: "NPS1"}
	if mode, ok := g.attr("current_compute_partition"); ok && mode != "" {
		d.Partition.Compute = mode
	}
}

var rootComplex = regexp.MustCompile(`pci[0-9a-f]{4}:[0-9a-f]{2}`)

// devices lists the captured cards and groups them by PCIe root complex.
func devices(gpus []*gpu, model uint32, keepIDs bool) ([]profile.Device, profile.Topology) {
	var devs []profile.Device
	roots := map[string]*profile.RootComplex{}
	for i, g := range gpus {
		d := profile.Device{
			Index: i, BDF: g.bdf, RenderMinor: int(g.props["drm_render_minor"]),
			UUID:     fmt.Sprintf("GPU-amd-%04x-0000-0000-%012d", model, i),
			UniqueID: fmt.Sprintf("0x%016x", 0xfa4e000000000000+uint64(i)+1),
		}
		if keepIDs {
			d.UniqueID = fmt.Sprintf("0x%016x", g.props["unique_id"])
		}
		d.XGMIID, _ = g.attr("xgmi_device_id")
		devs = append(devs, d)

		id, numa := "pci0000:00", 0
		if resolved, err := filepath.EvalSymlinks(g.dirs[0]); err == nil {
			if m := rootComplex.FindString(resolved); m != "" {
				id = m
			}
		}
		// -1 means no NUMA affinity; the mock topology needs a real node.
		if s, ok := g.attr("numa_node"); ok {
			if n, err := strconv.Atoi(s); err == nil && n >= 0 {
				numa = n
			}
		}
		if roots[id] == nil {
			roots[id] = &profile.RootComplex{ID: id, NUMA: numa}
		}
		roots[id].Devices = append(roots[id].Devices, g.bdf)
	}
	ids := make([]string, 0, len(roots))
	for id := range roots {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var t profile.Topology
	for _, id := range ids {
		t.RootComplexes = append(t.RootComplexes, *roots[id])
	}
	return devs, t
}

// readProps parses the "key value" lines of a KFD property file.
func readProps(path string) map[string]uint64 {
	props := map[string]uint64{}
	data, _ := os.ReadFile(path)
	for _, line := range strings.Split(string(data), "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			if v, err := strconv.ParseUint(f[1], 0, 64); err == nil {
				props[f[0]] = v
			}
		}
	}
	return props
}

var dpmLevel = regexp.MustCompile(`(?i)(\d+)\s*mhz\s*(\*?)`)

// levels reads a pp_dpm_* table such as "0: 600Mhz *\n1: 2200Mhz".
func levels(g *gpu, name string) (lo, hi, current uint32, ok bool) {
	table, _ := g.attr(name)
	for _, m := range dpmLevel.FindAllStringSubmatch(table, -1) {
		v, _ := strconv.Atoi(m[1])
		mhz := uint32(v)
		if !ok || mhz < lo {
			lo = mhz
		}
		hi = max(hi, mhz)
		if m[2] == "*" {
			current = mhz
		}
		ok = true
	}
	return
}

// linkGen maps a link speed such as "32.0 GT/s PCIe" to its generation.
func linkGen(g *gpu, name string) uint32 {
	speed, _ := g.attr(name)
	for gen, prefix := range []string{"2.5 ", "5.0 ", "8.0 ", "16.0 ", "32.0 ", "64.0 "} {
		if strings.HasPrefix(speed, prefix) {
			return uint32(gen + 1)
		}
	}
	return 0
}
