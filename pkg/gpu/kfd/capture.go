package kfd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// CaptureOptions steers CaptureProfile.
type CaptureOptions struct {
	BDF          string // reference GPU; empty picks the one with the most VRAM
	Name         string // marketing name; empty falls back to sysfs product_name
	Architecture string
	KeepIDs      bool // keep the cards' real unique IDs instead of placeholders
}

const amdVendorID = 0x1002

type capturedGPU struct {
	props, bank map[string]uint64
	bdf         string
	xgmiLinks   uint32
	dirs        []string // sysfs directories holding this device's attributes
}

// attr returns the trimmed content of the first readable attribute file.
// A runtime-suspended GPU answers EBUSY for live sensors, hence "readable".
func (g *capturedGPU) attr(name string) (string, bool) {
	for _, dir := range g.dirs {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			return strings.TrimSpace(string(b)), true
		}
	}
	return "", false
}

func (g *capturedGPU) num(name string) (uint64, bool) {
	s, ok := g.attr(name)
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.Fields(s + " x")[0], 0, 64)
	return v, err == nil
}

// CaptureProfile builds a profile from the amdgpu sysfs tree under root
// ("/" on a real machine). A profile describes one GPU model, so cards of
// another model than the reference GPU are left out and reported in notes,
// as is every value that could not be read.
func CaptureProfile(root string, o CaptureOptions) (*Profile, []string, error) {
	sys := filepath.Join(root, "sys")
	nodeDirs, _ := filepath.Glob(filepath.Join(sys, "class/kfd/kfd/topology/nodes/*"))
	sort.Slice(nodeDirs, func(a, b int) bool {
		x, _ := strconv.Atoi(filepath.Base(nodeDirs[a]))
		y, _ := strconv.Atoi(filepath.Base(nodeDirs[b]))
		return x < y
	})
	var all []*capturedGPU
	for _, dir := range nodeDirs {
		props := readProps(filepath.Join(dir, "properties"))
		if props["vendor_id"] != amdVendorID || props["simd_count"] == 0 {
			continue // the CPU node, or a KFD node we cannot read
		}
		loc := props["location_id"]
		g := &capturedGPU{props: props, bank: readProps(filepath.Join(dir, "mem_banks/0/properties")),
			bdf: fmt.Sprintf("%04x:%02x:%02x.%x", props["domain"], loc>>8&0xff, loc>>3&0x1f, loc&7)}
		links, _ := filepath.Glob(filepath.Join(dir, "io_links/*/properties"))
		for _, link := range links {
			if readProps(link)["type"] == 11 {
				g.xgmiLinks++
			}
		}
		g.dirs = []string{filepath.Join(sys, "bus/pci/devices", g.bdf)}
		cards, _ := filepath.Glob(filepath.Join(sys, "class/drm/card*/device"))
		for _, card := range cards {
			if b, err := os.ReadFile(filepath.Join(card, "uevent")); err == nil && strings.Contains(string(b), "PCI_SLOT_NAME="+g.bdf) {
				g.dirs = append(g.dirs, card)
			}
		}
		for _, dir := range append([]string{}, g.dirs...) {
			hwmon, _ := filepath.Glob(filepath.Join(dir, "hwmon/hwmon*"))
			g.dirs = append(g.dirs, hwmon...)
		}
		all = append(all, g)
	}
	if len(all) == 0 {
		return nil, nil, fmt.Errorf("no AMD GPU in %s/class/kfd/kfd/topology; is the amdgpu driver loaded?", sys)
	}

	// Dedicated VRAM decides: KFD's memory bank of an integrated GPU counts
	// shared system memory and would outrank a discrete card.
	vram := func(g *capturedGPU) uint64 {
		if v, ok := g.num("mem_info_vram_total"); ok {
			return v
		}
		return g.bank["size_in_bytes"]
	}
	ref := all[0]
	for _, g := range all {
		if o.BDF == g.bdf || (o.BDF == "" && vram(g) > vram(ref)) {
			ref = g
		}
	}
	if o.BDF != "" && ref.bdf != o.BDF {
		return nil, nil, fmt.Errorf("no AMD GPU at %s", o.BDF)
	}
	var notes []string
	var gpus []*capturedGPU
	for _, g := range all {
		if g.props["device_id"] == ref.props["device_id"] {
			gpus = append(gpus, g)
		} else {
			notes = append(notes, fmt.Sprintf("left out %s (device 0x%04x): another model, capture it with --gpu %s", g.bdf, g.props["device_id"], g.bdf))
		}
	}
	// get reads a number from the reference GPU and records what was missing.
	get := func(what, name string, scale uint64) uint64 {
		v, ok := ref.num(name)
		if !ok {
			notes = append(notes, fmt.Sprintf("%s unknown: %s not readable", what, name))
		}
		return v / scale
	}

	p := &Profile{Version: "1.0"}
	if v, err := os.ReadFile(filepath.Join(sys, "module/amdgpu/version")); err == nil {
		p.System.AMDGPUVersion = strings.TrimSpace(string(v))
	} else if v, err := os.ReadFile(filepath.Join(root, "proc/sys/kernel/osrelease")); err == nil {
		p.System.AMDGPUVersion = strings.TrimSpace(string(v)) // in-tree driver
	}

	d := &p.DeviceDefault
	product, _ := ref.attr("product_name")
	d.ProductName = product
	if d.Name = o.Name; d.Name == "" {
		if d.Name = product; d.Name == "" {
			d.Name = fmt.Sprintf("AMD GPU 1002:%04x", ref.props["device_id"])
			notes = append(notes, "name is a placeholder: no product_name in sysfs, pass --name")
		}
	}
	if d.Architecture = o.Architecture; d.Architecture == "" {
		d.Architecture = "unknown"
	}
	d.VendorID, d.DeviceID = amdVendorID, uint32(ref.props["device_id"])
	d.SubsystemVendorID = uint32(get("subsystem vendor", "subsystem_vendor", 1))
	d.SubsystemDeviceID = uint32(get("subsystem device", "subsystem_device", 1))
	d.GFXTargetVersion = uint32(ref.props["gfx_target_version"])

	k := ref.props
	d.Compute = ComputeConfig{
		SIMDPerCU: uint32(k["simd_per_cu"]), SIMDCount: uint32(k["simd_count"]),
		WaveFrontSize: uint32(k["wave_front_size"]), ArrayCount: uint32(k["array_count"]),
		SIMDArraysPerEng: uint32(k["simd_arrays_per_engine"]), CUPerSIMDArray: uint32(k["cu_per_simd_array"]),
		NumXCC: uint32(k["num_xcc"]), MaxWavesPerSIMD: uint32(k["max_waves_per_simd"]),
		LDSSizeKB: uint32(k["lds_size_in_kb"]), GDSSizeKB: uint32(k["gds_size_in_kb"]),
		NumSDMAEngines: uint32(k["num_sdma_engines"]), NumSDMAXGMIEngines: uint32(k["num_sdma_xgmi_engines"]),
		NumCPQueues: uint32(k["num_cp_queues"]),
	}
	if d.Compute.SIMDPerCU > 0 {
		d.Compute.CUCount = d.Compute.SIMDCount / d.Compute.SIMDPerCU
		d.Compute.StreamProcessors = d.Compute.CUCount * d.Compute.WaveFrontSize
	}

	vendor, _ := ref.attr("mem_info_vram_vendor")
	d.Memory = MemoryConfig{VRAMType: vendor, VRAMSizeBytes: ref.bank["size_in_bytes"],
		VRAMBusWidth: uint32(ref.bank["width"]), MaxMemClkMHz: uint32(ref.bank["mem_clk_max"])}
	if v, ok := ref.num("mem_info_vram_total"); ok {
		d.Memory.VRAMSizeBytes = v
	}
	d.Memory.VRAMUsedBytes, _ = ref.num("mem_info_vram_used")
	if d.Memory.VisVRAMBytes, _ = ref.num("mem_info_vis_vram_total"); d.Memory.VisVRAMBytes == 0 {
		d.Memory.VisVRAMBytes = d.Memory.VRAMSizeBytes
	}
	d.Memory.GTTSizeBytes, _ = ref.num("mem_info_gtt_total")

	const microwatt, millidegree = 1_000_000, 1000
	d.Power.MaxPowerCapW = uint32(get("power cap", "power1_cap_max", microwatt))
	if d.Power.MaxPowerCapW == 0 {
		notes = append(notes, "max_power_cap_w is 0, so the mock cannot derive power or temperature: set it by hand, or capture again while the GPU is active (a suspended GPU hides its sensors)")
	}
	if v, ok := ref.num("power1_cap_default"); ok {
		d.Power.DefaultPowerCapW = uint32(v / microwatt)
	} else if v, ok := ref.num("power1_cap"); ok {
		d.Power.DefaultPowerCapW = uint32(v / microwatt)
	}
	if v, ok := ref.num("power1_cap_min"); ok {
		d.Power.MinPowerCapW = uint32(v / microwatt)
	}
	if v, ok := ref.num("power1_average"); ok {
		d.Power.CurrentSocketPowerW = uint32(v / microwatt)
	} else if v, ok := ref.num("power1_input"); ok {
		d.Power.CurrentSocketPowerW = uint32(v / microwatt)
	}

	d.Thermal.EdgeTempC = uint32(get("edge temperature", "temp1_input", millidegree))
	if v, ok := ref.num("temp2_input"); ok {
		d.Thermal.HotspotTempC = uint32(v / millidegree)
	}
	if v, ok := ref.num("temp3_input"); ok {
		d.Thermal.MemTempC = uint32(v / millidegree)
	}
	if v, ok := ref.num("temp1_crit"); ok {
		d.Thermal.SlowdownTempC = uint32(v / millidegree)
	}
	if v, ok := ref.num("temp1_emergency"); ok {
		d.Thermal.ShutdownTempC = uint32(v / millidegree)
	}

	var okClk bool
	if d.Clocks.MinGFXClkMHz, d.Clocks.MaxGFXClkMHz, d.Clocks.CurrentGFXClkMHz, okClk = dpmLevels(ref, "pp_dpm_sclk"); !okClk {
		notes = append(notes, "GFX clock range unknown: pp_dpm_sclk not readable")
	}
	d.Clocks.MinMemClkMHz, d.Clocks.MaxMemClkMHz, d.Clocks.CurrentMemClkMHz, _ = dpmLevels(ref, "pp_dpm_mclk")
	if d.Clocks.MaxMemClkMHz == 0 {
		d.Clocks.MaxMemClkMHz = d.Memory.MaxMemClkMHz
		d.Clocks.CurrentMemClkMHz = d.Memory.MaxMemClkMHz
	}

	d.PCIe.Gen, d.PCIe.Width = linkGen(ref, "current_link_speed"), uint32(get("PCIe width", "current_link_width", 1))
	d.PCIe.MaxGen = linkGen(ref, "max_link_speed")
	if v, ok := ref.num("max_link_width"); ok {
		d.PCIe.MaxWidth = uint32(v)
	}

	if _, err := os.Stat(filepath.Join(ref.dirs[0], "ras")); err == nil {
		d.ECC.Enabled = true
	}
	d.Firmware.PMFWVersion, _ = ref.attr("fw_version/smc_fw_version")
	d.Partition = PartitionConfig{Mode: "SPX", NPSMode: "NPS1"}
	if mode, ok := ref.attr("current_compute_partition"); ok && mode != "" {
		d.Partition.Mode = mode
	}

	if hive := ref.props["hive_id"]; hive != 0 || ref.xgmiLinks > 0 {
		p.XGMI = XGMIConfig{LinksPerGPU: ref.xgmiLinks, LinkType: 11}
		if hive != 0 {
			p.XGMI.HiveID = fmt.Sprintf("0x%016x", hive)
		}
	}

	complexes := map[string]*RootComplex{}
	for i, g := range gpus {
		dev := DeviceConfig{Index: i, PCIBDF: g.bdf, DRMRenderMinor: int(g.props["drm_render_minor"]),
			UUID:     fmt.Sprintf("GPU-amd-%04x-0000-0000-%012d", d.DeviceID, i),
			UniqueID: fmt.Sprintf("0x%016x", 0xfa4e000000000000+uint64(i)+1)}
		if o.KeepIDs {
			dev.UniqueID = fmt.Sprintf("0x%016x", g.props["unique_id"])
		}
		dev.XGMIDeviceID, _ = g.attr("xgmi_device_id")
		p.Devices = append(p.Devices, dev)

		id, numa := "pci0000:00", 0
		if resolved, err := filepath.EvalSymlinks(g.dirs[0]); err == nil {
			if m := regexp.MustCompile(`pci[0-9a-f]{4}:[0-9a-f]{2}`).FindString(resolved); m != "" {
				id = m
			}
		}
		if s, ok := g.attr("numa_node"); ok {
			// -1 means "no NUMA affinity"; the mock topology needs a real node.
			if n, err := strconv.Atoi(s); err == nil && n >= 0 {
				numa = n
			}
		}
		if complexes[id] == nil {
			complexes[id] = &RootComplex{ID: id, NUMANode: numa}
		}
		complexes[id].Devices = append(complexes[id].Devices, g.bdf)
	}
	ids := make([]string, 0, len(complexes))
	for id := range complexes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		p.PCIETopology.RootComplexes = append(p.PCIETopology.RootComplexes, *complexes[id])
	}
	return p, notes, nil
}

// readProps parses the "key value" lines KFD uses for its property files.
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

// dpmLevels reads a pp_dpm_* table such as "0: 600Mhz *\n1: 2200Mhz".
func dpmLevels(g *capturedGPU, name string) (lo, hi, current uint32, ok bool) {
	table, _ := g.attr(name)
	for _, m := range dpmLevel.FindAllStringSubmatch(table, -1) {
		v, _ := strconv.Atoi(m[1])
		mhz := uint32(v)
		if !ok || mhz < lo {
			lo = mhz
		}
		if mhz > hi {
			hi = mhz
		}
		if m[2] == "*" {
			current = mhz
		}
		ok = true
	}
	return
}

// linkGen maps a sysfs link speed such as "32.0 GT/s PCIe" to its generation.
func linkGen(g *capturedGPU, name string) uint32 {
	speed, _ := g.attr(name)
	for gen, prefix := range []string{"2.5 ", "5.0 ", "8.0 ", "16.0 ", "32.0 ", "64.0 "} {
		if strings.HasPrefix(speed, prefix) {
			return uint32(gen + 1)
		}
	}
	return 0
}
