package sysfs

import (
	"fmt"
	"path"
	"strings"

	"github.com/mesutoezdil/fake-gpu/pkg/profile"
)

// layout is a tree to write: files and symlinks by path below the root.
type layout struct {
	files map[string]string
	links map[string]string // link -> target, both relative to the root
}

func newLayout() *layout {
	return &layout{files: map[string]string{}, links: map[string]string{}}
}

func (l *layout) file(p, content string)      { l.files[p] = content }
func (l *layout) line(p, value string)        { l.files[p] = value + "\n" }
func (l *layout) lines(p string, v ...string) { l.files[p] = strings.Join(v, "\n") + "\n" }
func (l *layout) link(p, target string)       { l.links[p] = target }

func hex4(v uint32) string { return fmt.Sprintf("0x%04x", v) }

// Paths shared by the static layout and the runtime updates.
const (
	topology = "sys/class/kfd/kfd/topology"
	module   = "sys/module/amdgpu"
	bound    = module + "/drivers/pci:amdgpu"
	pciDrv   = "sys/bus/pci/drivers/amdgpu"
)

func card(index int) string           { return fmt.Sprintf("sys/class/drm/card%d/device", index) }
func node(pos int) string             { return fmt.Sprintf("%s/nodes/%d", topology, pos) }
func xcp(index int) string            { return fmt.Sprintf("sys/devices/platform/amdgpu_xcp_%d", index) }
func pciDevice(rc, bdf string) string { return path.Join("sys/devices", rc, bdf) }

// build lays out every static surface of p: KFD topology, DRM cards, the
// amdgpu module and its bound devices, PCI devices, driver links and the
// host files kind mounts over.
func build(p *profile.Profile) (*layout, error) {
	l := newLayout()
	if err := kfd(l, p); err != nil {
		return nil, err
	}
	for _, d := range p.Devices {
		if err := drm(l, p, d); err != nil {
			return nil, err
		}
	}
	driver(l, p)
	for _, d := range p.Devices {
		bind(l, p, d)
		pci(l, p, d)
	}
	host(l, p)
	return l, nil
}

// kfd writes the topology KFD exposes: node 0 is the CPU, GPU nodes follow.
func kfd(l *layout, p *profile.Profile) error {
	hive, err := profile.ParseID(p.XGMI.Hive)
	if err != nil {
		return fmt.Errorf("invalid xgmi.hive_id: %w", err)
	}
	l.line(topology+"/generation_id", "1")
	l.lines(topology+"/system_properties", "platform_oem 0", "platform_id 0", "platform_rev 0")
	l.line(node(0)+"/gpu_id", "0")
	l.line(node(0)+"/name", "CPU")
	l.lines(node(0)+"/properties", cpuProperties...)
	for pos, d := range p.Devices {
		if err := gpuNode(l, p, pos+1, d, hive); err != nil {
			return fmt.Errorf("gpu node %d: %w", pos, err)
		}
	}
	return nil
}

// cpuProperties is the CPU node of a 128-core host.
var cpuProperties = func() []string {
	props := []string{"cpu_cores_count 128"}
	for _, k := range []string{"simd_count", "mem_banks_count 1", "caches_count", "io_links_count", "p2p_links_count",
		"cpu_core_id_base", "simd_id_base", "max_waves_per_simd", "lds_size_in_kb", "gds_size_in_kb", "num_gws",
		"wave_front_size", "array_count", "simd_arrays_per_engine", "cu_per_simd_array", "simd_per_cu",
		"max_slots_scratch_cu", "gfx_target_version", "vendor_id", "device_id", "location_id", "domain",
		"drm_render_minor", "hive_id", "num_sdma_engines", "num_sdma_xgmi_engines", "num_sdma_queues_per_engine",
		"num_cp_queues", "max_engine_clk_fcompute", "local_mem_size", "fw_version", "capability", "debug_prop",
		"sdma_fw_version", "unique_id", "num_xcc"} {
		if !strings.Contains(k, " ") {
			k += " 0"
		}
		props = append(props, k)
	}
	return props
}()

func gpuNode(l *layout, p *profile.Profile, pos int, d profile.Device, hive uint64) error {
	// KFD prints unique_id and hive_id in decimal, even where a profile
	// holds hex; a decimal-only reader would take 0x... for zero.
	unique, err := profile.ParseID(d.UniqueID)
	if err != nil {
		return fmt.Errorf("invalid unique_id for GPU %d: %w", d.Index, err)
	}
	c, k := p.Card, p.Card.Compute
	bdf := profile.ParseBDF(d.BDF)
	dir := node(pos)
	links := peers(p, d)
	l.line(dir+"/gpu_id", fmt.Sprint(d.Index+1))
	l.line(dir+"/name", c.Name)
	l.lines(dir+"/properties",
		"cpu_cores_count 0",
		fmt.Sprintf("simd_count %d", k.SIMDs),
		"mem_banks_count 1",
		"caches_count 0",
		fmt.Sprintf("io_links_count %d", 1+len(links)),
		"p2p_links_count 0",
		"cpu_core_id_base 0",
		"simd_id_base 0",
		fmt.Sprintf("max_waves_per_simd %d", k.WavesPerSIMD),
		fmt.Sprintf("lds_size_in_kb %d", k.LDSKiB),
		fmt.Sprintf("gds_size_in_kb %d", k.GDSKiB),
		"num_gws 0",
		fmt.Sprintf("wave_front_size %d", k.Wavefront),
		fmt.Sprintf("array_count %d", k.Arrays),
		fmt.Sprintf("simd_arrays_per_engine %d", k.ArraysPerEngine),
		fmt.Sprintf("cu_per_simd_array %d", k.CUsPerArray),
		fmt.Sprintf("simd_per_cu %d", k.SIMDPerCU),
		"max_slots_scratch_cu 32",
		fmt.Sprintf("gfx_target_version %d", c.GFXTarget),
		fmt.Sprintf("vendor_id %d", c.Vendor),
		fmt.Sprintf("device_id %d", c.Device),
		fmt.Sprintf("location_id %d", bdf.Location()),
		fmt.Sprintf("domain %d", bdf.Domain),
		fmt.Sprintf("drm_render_minor %d", d.RenderMinor),
		fmt.Sprintf("hive_id %d", hive),
		fmt.Sprintf("num_sdma_engines %d", k.SDMAEngines),
		fmt.Sprintf("num_sdma_xgmi_engines %d", k.SDMAXGMIEngines),
		"num_sdma_queues_per_engine 4",
		fmt.Sprintf("num_cp_queues %d", k.CPQueues),
		fmt.Sprintf("max_engine_clk_fcompute %d", c.Clocks.GFXMax),
		fmt.Sprintf("local_mem_size %d", c.Memory.VRAM),
		"fw_version 0",
		"capability 0x00002b3a",
		"debug_prop 0x0000000000000900",
		"sdma_fw_version 0",
		fmt.Sprintf("unique_id %d", unique),
		fmt.Sprintf("num_xcc %d", k.XCCs),
	)
	l.lines(dir+"/mem_banks/0/properties",
		"heap_type 1",
		fmt.Sprintf("size_in_bytes %d", c.Memory.VRAM),
		"flags 0",
		fmt.Sprintf("width %d", c.Memory.BusWidth),
		fmt.Sprintf("mem_clk_max %d", c.Memory.MaxClockMHz),
	)
	// Link 0 is PCIe to the CPU, then one xGMI link per peer card.
	l.file(dir+"/io_links/0/properties", ioLink(2, d.Index+1, 0, 40, 0, true))
	for i, peer := range links {
		l.file(fmt.Sprintf("%s/io_links/%d/properties", dir, i+1), xgmiLink(p, d, peer, true))
	}
	l.line(card(d.Index)+"/numa_node", fmt.Sprint(p.NUMA(d.BDF)))
	return nil
}

// peers are the devices on other cards; partitions of one card share no
// xGMI link.
func peers(p *profile.Profile, d profile.Device) []profile.Device {
	var out []profile.Device
	for _, peer := range p.Devices {
		if peer.BDF != d.BDF {
			out = append(out, peer)
		}
	}
	return out
}

func ioLink(kind uint32, from, to, weight int, bandwidthMBps uint32, up bool) string {
	flags := 0
	if up {
		flags = 1
	}
	return strings.Join([]string{
		fmt.Sprintf("type %d", kind),
		"version_major 0",
		"version_minor 0",
		fmt.Sprintf("node_from %d", from),
		fmt.Sprintf("node_to %d", to),
		fmt.Sprintf("weight %d", weight),
		"min_latency 0",
		"max_latency 0",
		"min_bandwidth 0",
		fmt.Sprintf("max_bandwidth %d", bandwidthMBps),
		"recommended_transfer_size 0",
		fmt.Sprintf("flags 0x%016x", flags),
	}, "\n") + "\n"
}

func xgmiLink(p *profile.Profile, from, to profile.Device, up bool) string {
	return ioLink(p.XGMI.LinkType, from.Index+1, to.Index+1, 15, p.XGMI.LinkGBps*1000, up)
}

// drm writes /sys/class/drm/cardN/device: identity, the amdgpu attributes
// with their boot values, the hwmon sensors and the xGMI hive.
func drm(l *layout, p *profile.Profile, d profile.Device) error {
	c := p.Card
	unique, err := profile.ParseID(d.UniqueID)
	if err != nil {
		return fmt.Errorf("invalid unique_id for GPU %d: %w", d.Index, err)
	}
	dir := card(d.Index)
	l.line(dir+"/vendor", hex4(c.Vendor))
	l.line(dir+"/device", hex4(c.Device))
	// AMD's DRA driver (productName) and node labeller (product-name) read it.
	l.line(dir+"/product_name", c.ProductName())
	l.file(dir+"/uevent", uevent(c, d.BDF, "38000"))
	const uW, mC, Hz = 1_000_000, 1000, 1_000_000
	for name, v := range map[string]string{
		"subsystem_vendor":                  hex4(c.SubsystemVendor),
		"subsystem_device":                  hex4(c.SubsystemDevice),
		"unique_id":                         fmt.Sprintf("%016x", unique),
		"mem_info_vram_total":               fmt.Sprint(c.Memory.VRAM),
		"mem_info_vis_vram_total":           fmt.Sprint(c.Memory.VisibleVRAM),
		"mem_info_gtt_total":                fmt.Sprint(c.Memory.GTT),
		"mem_info_vram_used":                fmt.Sprint(c.Memory.VRAMUsed),
		"mem_info_vis_vram_used":            fmt.Sprint(c.Memory.VRAMUsed),
		"mem_info_gtt_used":                 "0",
		"current_link_speed":                linkSpeed(c.PCIe.Gen),
		"current_link_width":                fmt.Sprint(c.PCIe.Width),
		"max_link_speed":                    linkSpeed(c.PCIe.MaxGen),
		"max_link_width":                    fmt.Sprint(c.PCIe.MaxWidth),
		"gpu_busy_percent":                  fmt.Sprint(c.Utilization.GFX),
		"mem_busy_percent":                  fmt.Sprint(c.Utilization.UMC),
		"pp_dpm_sclk":                       dpm(c.Clocks.GFXMin, c.Clocks.GFXMax, c.Clocks.GFX),
		"pp_dpm_mclk":                       dpm(c.Clocks.MemMin, c.Clocks.MemMax, c.Clocks.Mem),
		"power_dpm_force_performance_level": "auto",
		"thermal_throttling_logging":        d.BDF + ": thermal throttling logging enabled, with interval 60 seconds",

		// The amdgpu hwmon device, labels included.
		"hwmon/hwmon0/name":               "amdgpu",
		"hwmon/hwmon0/temp1_label":        "edge",
		"hwmon/hwmon0/temp2_label":        "junction",
		"hwmon/hwmon0/temp3_label":        "mem",
		"hwmon/hwmon0/temp1_input":        fmt.Sprint(c.Thermal.Edge * mC),
		"hwmon/hwmon0/temp2_input":        fmt.Sprint(c.Thermal.Hotspot * mC),
		"hwmon/hwmon0/temp3_input":        fmt.Sprint(c.Thermal.Mem * mC),
		"hwmon/hwmon0/temp1_crit":         fmt.Sprint(c.Thermal.Slowdown * mC),
		"hwmon/hwmon0/temp2_crit":         fmt.Sprint(c.Thermal.Slowdown * mC),
		"hwmon/hwmon0/temp3_crit":         fmt.Sprint(c.Thermal.Slowdown * mC),
		"hwmon/hwmon0/temp1_emergency":    fmt.Sprint(c.Thermal.Shutdown * mC),
		"hwmon/hwmon0/power1_label":       "PPT",
		"hwmon/hwmon0/power1_average":     fmt.Sprint(uint64(c.Power.Socket) * uW),
		"hwmon/hwmon0/power1_cap":         fmt.Sprint(uint64(c.Power.DefaultCap) * uW),
		"hwmon/hwmon0/power1_cap_default": fmt.Sprint(uint64(c.Power.DefaultCap) * uW),
		"hwmon/hwmon0/power1_cap_max":     fmt.Sprint(uint64(c.Power.MaxCap) * uW),
		"hwmon/hwmon0/power1_cap_min":     fmt.Sprint(uint64(c.Power.MinCap) * uW),
		"hwmon/hwmon0/freq1_label":        "sclk",
		"hwmon/hwmon0/freq2_label":        "mclk",
		"hwmon/hwmon0/freq1_input":        fmt.Sprint(uint64(c.Clocks.GFX) * Hz),
		"hwmon/hwmon0/freq2_input":        fmt.Sprint(uint64(c.Clocks.Mem) * Hz),
	} {
		l.line(dir+"/"+name, v)
	}
	if p.XGMI.Hive != "" {
		l.line(dir+"/xgmi_hive_info/xgmi_hive_id", p.XGMI.Hive)
		l.line(dir+"/xgmi_device_id", d.XGMIID)
		l.line(dir+"/xgmi_error", "0")
	}
	l.file(dir+"/ras/gpu_vram_bad_pages", "")
	l.link(dir+"/driver", pciDrv)
	return nil
}

func uevent(c profile.Card, bdf, class string) string {
	return fmt.Sprintf("DRIVER=amdgpu\nPCI_CLASS=%s\nPCI_ID=1002:%04X\nPCI_SUBSYS_ID=1002:%04X\nPCI_SLOT_NAME=%s\n",
		class, c.Device, c.SubsystemDevice, bdf)
}

// driver writes the amdgpu module. amd-smi needs initstate "live", and AMD's
// DRA driver and node labeller read the version through the driver links.
func driver(l *layout, p *profile.Profile) {
	l.line(module+"/refcnt", "1")
	l.line(module+"/version", p.Software.Driver)
	l.line(module+"/initstate", "live")
	l.link(pciDrv+"/module", module)
}

// bind lays out what amdgpu binds for one device. AMD's device plugin walks
// pci:amdgpu/<BDF>/drm/cardN/device/vendor and wants 0x1002 there. A
// partition other than the first is an amdgpu_xcp platform device instead.
func bind(l *layout, p *profile.Profile, d profile.Device) {
	c := p.Card
	render := fmt.Sprintf("renderD%d", d.RenderMinor)
	cardN := fmt.Sprintf("card%d", d.Index)
	if d.Slice > 0 {
		for _, name := range []string{cardN, render} {
			l.line(xcp(d.Index)+"/drm/"+name+"/device/vendor", hex4(c.Vendor))
		}
		return
	}
	dir := bound + "/" + d.BDF
	l.line(dir+"/numa_node", fmt.Sprint(p.NUMA(d.BDF)))
	l.line(dir+"/current_compute_partition", c.Partition.Compute)
	l.line(dir+"/current_memory_partition", c.Partition.Memory)
	l.line(dir+"/vendor", hex4(c.Vendor))
	l.line(dir+"/device", hex4(c.Device))
	l.file(dir+"/uevent", uevent(c, d.BDF, "038000"))
	l.line(dir+"/drm/"+cardN+"/device/vendor", hex4(c.Vendor))
	l.line(dir+"/drm/"+render+"/device/vendor", hex4(c.Vendor))
}

// pci writes the PCI device under its root complex, the bus link to it, and
// the links between it and the amdgpu driver.
func pci(l *layout, p *profile.Profile, d profile.Device) {
	rc, ok := p.Root(d.BDF)
	if !ok {
		return
	}
	c := p.Card
	dir := pciDevice(rc.ID, d.BDF)
	l.line(dir+"/vendor", hex4(c.Vendor))
	l.line(dir+"/device", hex4(c.Device))
	l.line(dir+"/class", "0x038000")
	l.line(dir+"/numa_node", fmt.Sprint(rc.NUMA))
	l.link("sys/bus/pci/devices/"+d.BDF, dir)
	l.link(dir+"/driver", pciDrv)
	l.link(pciDrv+"/"+d.BDF, dir)
}

// host covers the paths a container runtime or kind bind-mounts over /sys;
// without them containerd fails with "mount point does not exist".
func host(l *layout, p *profile.Profile) {
	const zero = "00000000-0000-0000-0000-000000000000"
	l.line("sys/class/dmi/id/product_name", p.Card.Name)
	l.line("sys/class/dmi/id/board_vendor", "AMD")
	l.line("sys/class/dmi/id/product_serial", "mock-serial")
	l.line("sys/class/dmi/id/product_uuid", zero)
	l.line("sys/devices/virtual/dmi/id/product_uuid", zero) // kind mounts the host UUID here
	l.line("sys/devices/system/node/node0/cpulist", "0-127")
}

// linkSpeed is a PCIe generation as sysfs prints link speed.
func linkSpeed(gen uint32) string {
	speeds := []string{"Unknown", "2.5 GT/s PCIe", "5.0 GT/s PCIe", "8.0 GT/s PCIe", "16.0 GT/s PCIe", "32.0 GT/s PCIe", "64.0 GT/s PCIe"}
	if int(gen) >= len(speeds) {
		return speeds[0]
	}
	return speeds[gen]
}

// dpm is a pp_dpm_* table; the star marks the level in use.
func dpm(lo, hi, current uint32) string {
	if lo == 0 || lo >= hi {
		return fmt.Sprintf("0: %dMhz *", max(lo, hi))
	}
	if 2*uint64(current) >= uint64(lo)+uint64(hi) {
		return fmt.Sprintf("0: %dMhz\n1: %dMhz *", lo, hi)
	}
	return fmt.Sprintf("0: %dMhz *\n1: %dMhz", lo, hi)
}
