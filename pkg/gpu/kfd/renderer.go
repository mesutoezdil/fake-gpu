package kfd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// Renderer writes the KFD sysfs topology tree and device nodes from a Profile.
type DeviceBackend interface {
	Reconcile(*Profile) error
	RestoreDevice(int) error
	RemoveDevice(int) error
}

type Renderer struct {
	backend DeviceBackend
	profile *Profile
	rootDir string // e.g. /var/lib/fake-gpu

	mu         sync.Mutex
	xgmiDown   map[int]bool   // KFD node IDs whose xGMI links are down
	kernelLog  string         // file that receives one line per fault transition
	lastStatus map[int]string // by GPU index, to log only changes
	kernelSeq  uint64         // event number in RAS lines
	hostDev    string         // device directory to manage on crash and recovery; empty leaves the host alone
}

// SetHostDevDir lets crash and recovery remove and recreate card and render
// nodes in dir, normally /dev. Without it the renderer only writes below its
// own root, which is what tests and unprivileged runs want.
func (r *Renderer) SetHostDevDir(dir string) { r.hostDev = dir }

// SetKernelLog makes fault transitions append amdgpu-style lines to path.
// /dev/kmsg puts them into the node's real kernel log, so it is opt-in.
func (r *Renderer) SetKernelLog(path string) { r.kernelLog = path }

// logTransition records a status change in the kernel log. Where the amdgpu
// driver has a message for the event, the line uses the driver's own format
// (taken from the 7.0 module) so log parsers match it. Events the driver has
// no line for are written under a fake-gpu tag instead of an invented format.
func (r *Renderer) logTransition(gpu *GPUState) {
	r.mu.Lock()
	last, seen := r.lastStatus[gpu.Index]
	if r.lastStatus == nil {
		r.lastStatus = map[int]string{}
	}
	r.lastStatus[gpu.Index] = gpu.Status
	r.kernelSeq++
	seq := r.kernelSeq
	r.mu.Unlock()
	if !seen {
		last = "healthy"
	}
	if r.kernelLog == "" || last == gpu.Status {
		return
	}
	ras := func(count int, block string) string {
		return fmt.Sprintf("amdgpu: {%d} socket: 0, die: 0, %d new uncorrectable hardware errors detected in %s block", seq, count, block)
	}
	var event string
	switch {
	case gpu.Status == "healthy" && last == "crashed":
		event = "amdgpu: GPU reset(1) succeeded!"
	case gpu.Status == "healthy":
		event = "fake-gpu: recovered"
	case gpu.Status == "crashed":
		event = "fake-gpu: device lost"
	case gpu.Status == "overheating":
		event = "amdgpu: ERROR: GPU over temperature range(SW CTF) detected!"
	case gpu.Status == "ecc_error":
		event = ras(gpu.ECCErrors, "umc")
	case gpu.Status == "throttling":
		event = "amdgpu: WARN: GPU thermal throttling temperature reached, expect performance decrease. TEMP_HOTSPOT."
	case gpu.Status == "page_retired":
		event = fmt.Sprintf("fake-gpu: %d VRAM page(s) retired", gpu.RetiredPages)
	case gpu.Status == "xgmi_link_down":
		event = ras(1, "xgmi_wafl")
	default:
		event = "fake-gpu: status " + gpu.Status
	}
	line := fmt.Sprintf("amdgpu %s: %s\n", gpu.PCIBDF, event)
	if r.kernelLog == "/dev/kmsg" {
		line = "<3>" + line // KERN_ERR
	}
	f, err := os.OpenFile(r.kernelLog, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(line)
}

// maxBadPages bounds the retired-page list one GPU can report.
const maxBadPages = 1024

func (r *Renderer) SetDeviceBackend(backend DeviceBackend) { r.backend = backend }

func NewRenderer(profile *Profile, rootDir string) *Renderer {
	return &Renderer{profile: profile, rootDir: rootDir}
}

// SwitchProfile cleans the old sysfs surfaces, loads a new profile, and
// re-renders the sysfs tree. Device nodes are not recreated: they don't
// change between profiles (same /dev/kfd and /dev/dri/renderD* paths).
func (r *Renderer) SwitchProfile(profile *Profile) error {
	if err := clearDirectory(filepath.Join(r.rootDir, "sys")); err != nil {
		return err
	}
	r.profile = profile
	if err := r.renderKFDTopology(); err != nil {
		return fmt.Errorf("kfd topology: %w", err)
	}
	if err := r.renderDRMDevices(); err != nil {
		return fmt.Errorf("drm devices: %w", err)
	}
	if err := r.renderDriverModule(); err != nil {
		return fmt.Errorf("driver module: %w", err)
	}
	if err := r.renderPCISysfs(); err != nil {
		return fmt.Errorf("pci sysfs: %w", err)
	}
	if err := r.renderDriverLinks(); err != nil {
		return fmt.Errorf("driver links: %w", err)
	}
	if err := r.renderHostCompat(); err != nil {
		return fmt.Errorf("host compat: %w", err)
	}
	if r.backend != nil {
		if err := r.backend.Reconcile(profile); err != nil {
			return err
		}
	}
	return r.renderSMIState()
}

// UpdateGPUSysfs writes runtime state changes for a single GPU back to the
// staged sysfs tree, so the device plugin and other consumers see the change.
func (r *Renderer) UpdateGPUSysfs(gpu *GPUState) error {
	r.logTransition(gpu)
	if err := r.WriteSMIState(gpu); err != nil {
		return err
	}
	nodeIdx := gpu.Index + 1 // KFD node 0 is CPU
	topoDir := filepath.Join(r.rootDir, "sys/class/kfd/kfd/topology")
	nodeDir := filepath.Join(topoDir, fmt.Sprintf("nodes/%d", nodeIdx))

	// Update the RAS/ECC status file: consumers check this for GPU health.
	rasDir := filepath.Join(nodeDir, "ras")
	if gpu.Status == "crashed" || gpu.ECCErrors > 0 {
		if err := writeFile(filepath.Join(rasDir, "fatal_error"), "1\n"); err != nil {
			return err
		}
	} else {
		if err := writeFile(filepath.Join(rasDir, "fatal_error"), "0\n"); err != nil {
			return err
		}
	}

	// Update ECC error counts in the properties-like ras directory.
	if err := writeFile(filepath.Join(rasDir, "ecc_uncorrectable"),
		fmt.Sprintf("%d\n", gpu.ECCErrors)); err != nil {
		return err
	}

	// amdgpu lists retired pages as "pfn : size : flags"; R marks a reserved page.
	drmDev := filepath.Join(r.rootDir, fmt.Sprintf("sys/class/drm/card%d/device", gpu.Index))
	var pages strings.Builder
	for i := 0; i < gpu.RetiredPages && i < maxBadPages; i++ {
		fmt.Fprintf(&pages, "0x%08x : 0x00001000 : R\n", 0x1000+i)
	}
	if err := writeFile(filepath.Join(drmDev, "ras/gpu_vram_bad_pages"), pages.String()); err != nil {
		return err
	}
	if err := r.setXGMILink(gpu.Index, nodeIdx, gpu.Status == "xgmi_link_down"); err != nil {
		return err
	}

	if err := r.WriteTelemetrySysfs(gpu); err != nil {
		return err
	}

	// Mark GPU as crashed by removing it from the driver binding if crashed.
	driverDir := filepath.Join(r.rootDir, "sys/module/amdgpu/drivers/pci:amdgpu")
	bdf := gpu.PCIBDF
	crashMarker := filepath.Join(driverDir, bdf, "gpu_crashed")

	// When crashed: remove device nodes AND the driver BDF directory.
	// The device plugin walks /sys/module/amdgpu/drivers/pci:amdgpu/<BDF>/drm/
	// to discover GPUs. Removing the BDF dir makes the GPU invisible to the
	// plugin. Removing /dev/dri/* prevents kubelet from mounting it.
	renderNode := filepath.Join(r.hostDev, fmt.Sprintf("dri/renderD%d", r.renderMinor(gpu.Index)))
	cardNode := filepath.Join(r.hostDev, fmt.Sprintf("dri/card%d", gpu.Index))
	bdfDriverDir := filepath.Join(driverDir, bdf)
	if r.profile.DeviceDefault.Partition.Mode == "DPX" {
		if gpu.PartitionIndex > 0 {
			bdfDriverDir = filepath.Join(r.rootDir, fmt.Sprintf("sys/devices/platform/amdgpu_xcp_%d", gpu.Index))
		} else {
			bdfDriverDir = filepath.Join(bdfDriverDir, "drm")
		}
	}

	if gpu.Status == "crashed" {
		writeFile(crashMarker, "1\n")
		if r.backend != nil {
			if err := r.backend.RemoveDevice(gpu.Index); err != nil {
				return err
			}
		} else if r.hostDev != "" {
			os.Remove(renderNode)
			os.Remove(cardNode)
		}
		os.RemoveAll(bdfDriverDir)
	} else {
		os.Remove(crashMarker)
		if r.backend != nil {
			if err := r.backend.RestoreDevice(gpu.Index); err != nil {
				return err
			}
		} else if r.hostDev != "" {
			if _, err := os.Stat(renderNode); os.IsNotExist(err) {
				syscall.Mknod(renderNode, syscall.S_IFCHR|0o660, int(uint64(226)<<8|uint64(r.renderMinor(gpu.Index))))
			}
			if _, err := os.Stat(cardNode); os.IsNotExist(err) {
				syscall.Mknod(cardNode, syscall.S_IFCHR|0o660, int(uint64(226)<<8|uint64(gpu.Index)))
			}
		}
		// Restore driver BDF directory if it was removed
		if _, err := os.Stat(bdfDriverDir); os.IsNotExist(err) {
			r.renderDriverBDF(gpu)
		}
	}

	return nil
}

// RenderAll writes every simulated surface.
func (r *Renderer) RenderAll() error {
	// Keep the /sys directory inode mounted by consumers while dropping stale
	// inventory from a previous profile or agent incarnation.
	if err := clearDirectory(filepath.Join(r.rootDir, "sys")); err != nil {
		return err
	}
	if err := r.renderKFDTopology(); err != nil {
		return fmt.Errorf("kfd topology: %w", err)
	}
	if err := r.renderDRMDevices(); err != nil {
		return fmt.Errorf("drm devices: %w", err)
	}
	if err := r.renderDeviceNodes(); err != nil {
		return fmt.Errorf("device nodes: %w", err)
	}
	if err := r.renderDriverModule(); err != nil {
		return fmt.Errorf("driver module: %w", err)
	}
	if err := r.renderPCISysfs(); err != nil {
		return fmt.Errorf("pci sysfs: %w", err)
	}
	if err := r.renderDriverLinks(); err != nil {
		return fmt.Errorf("driver links: %w", err)
	}
	if err := r.renderHostCompat(); err != nil {
		return fmt.Errorf("host compat: %w", err)
	}
	return r.renderSMIState()
}

// renderHostCompat creates sysfs paths that the container runtime or KIND
// expects to exist when /sys is mounted from our mock tree. Without these,
// containerd hooks fail with "mount point does not exist".
func (r *Renderer) renderHostCompat() error {
	compatPaths := map[string]string{
		"sys/class/dmi/id/product_name":   r.profile.DeviceDefault.Name + "\n",
		"sys/class/dmi/id/board_vendor":   "AMD\n",
		"sys/class/dmi/id/product_serial": "mock-serial\n",
		// kind bind-mounts the host UUID here on native x86 nodes.
		"sys/devices/virtual/dmi/id/product_uuid": "00000000-0000-0000-0000-000000000000\n",
		"sys/class/dmi/id/product_uuid":           "00000000-0000-0000-0000-000000000000\n",
		"sys/devices/system/node/node0/cpulist":   "0-127\n",
	}
	for path, content := range compatPaths {
		if err := writeFile(filepath.Join(r.rootDir, path), content); err != nil {
			return err
		}
	}
	return nil
}

func (r *Renderer) renderKFDTopology() error {
	topoDir := filepath.Join(r.rootDir, "sys/class/kfd/kfd/topology")

	if err := writeFile(filepath.Join(topoDir, "generation_id"), "1\n"); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(topoDir, "system_properties"),
		"platform_oem 0\nplatform_id 0\nplatform_rev 0\n"); err != nil {
		return err
	}

	// Node 0 is the CPU node.
	cpuDir := filepath.Join(topoDir, "nodes/0")
	if err := r.renderCPUNode(cpuDir); err != nil {
		return err
	}

	// GPU nodes start at index 1.
	for i, dev := range r.profile.Devices {
		nodeDir := filepath.Join(topoDir, fmt.Sprintf("nodes/%d", i+1))
		if err := r.renderGPUNode(nodeDir, &dev); err != nil {
			return fmt.Errorf("gpu node %d: %w", i, err)
		}
	}

	return nil
}

func (r *Renderer) renderCPUNode(dir string) error {
	props := []string{
		"cpu_cores_count 128",
		"simd_count 0",
		"mem_banks_count 1",
		"caches_count 0",
		"io_links_count 0",
		"p2p_links_count 0",
		"cpu_core_id_base 0",
		"simd_id_base 0",
		"max_waves_per_simd 0",
		"lds_size_in_kb 0",
		"gds_size_in_kb 0",
		"num_gws 0",
		"wave_front_size 0",
		"array_count 0",
		"simd_arrays_per_engine 0",
		"cu_per_simd_array 0",
		"simd_per_cu 0",
		"max_slots_scratch_cu 0",
		"gfx_target_version 0",
		"vendor_id 0",
		"device_id 0",
		"location_id 0",
		"domain 0",
		"drm_render_minor 0",
		"hive_id 0",
		"num_sdma_engines 0",
		"num_sdma_xgmi_engines 0",
		"num_sdma_queues_per_engine 0",
		"num_cp_queues 0",
		"max_engine_clk_fcompute 0",
		"local_mem_size 0",
		"fw_version 0",
		"capability 0",
		"debug_prop 0",
		"sdma_fw_version 0",
		"unique_id 0",
		"num_xcc 0",
	}

	if err := writeFile(filepath.Join(dir, "gpu_id"), "0\n"); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, "name"), "CPU\n"); err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, "properties"), strings.Join(props, "\n")+"\n")
}

func (r *Renderer) renderGPUNode(dir string, dev *DeviceConfig) error {
	// KFD sysfs emits unique_id and hive_id as decimal, even when a profile
	// stores hex. Decimal-only clients otherwise read a 0x-prefixed ID as zero.
	uniqueID, err := parseID(dev.UniqueID)
	if err != nil {
		return fmt.Errorf("invalid unique_id for GPU %d: %w", dev.Index, err)
	}
	hiveID, err := parseID(r.profile.XGMI.HiveID)
	if err != nil {
		return fmt.Errorf("invalid xgmi.hive_id: %w", err)
	}
	d := r.profile.DeviceDefault
	c := d.Compute

	numaNode := r.numaForBDF(dev.PCIBDF)
	locationID := bdfToLocationID(dev.PCIBDF)
	domain := bdfToDomain(dev.PCIBDF)

	gpuID := uint32(dev.Index + 1)

	props := []string{
		"cpu_cores_count 0",
		fmt.Sprintf("simd_count %d", c.SIMDCount),
		"mem_banks_count 1",
		"caches_count 0",
		fmt.Sprintf("io_links_count %d", r.ioLinkCount(dev)),
		"p2p_links_count 0",
		"cpu_core_id_base 0",
		"simd_id_base 0",
		fmt.Sprintf("max_waves_per_simd %d", c.MaxWavesPerSIMD),
		fmt.Sprintf("lds_size_in_kb %d", c.LDSSizeKB),
		fmt.Sprintf("gds_size_in_kb %d", c.GDSSizeKB),
		"num_gws 0",
		fmt.Sprintf("wave_front_size %d", c.WaveFrontSize),
		fmt.Sprintf("array_count %d", c.ArrayCount),
		fmt.Sprintf("simd_arrays_per_engine %d", c.SIMDArraysPerEng),
		fmt.Sprintf("cu_per_simd_array %d", c.CUPerSIMDArray),
		fmt.Sprintf("simd_per_cu %d", c.SIMDPerCU),
		"max_slots_scratch_cu 32",
		fmt.Sprintf("gfx_target_version %d", d.GFXTargetVersion),
		fmt.Sprintf("vendor_id %d", d.VendorID),
		fmt.Sprintf("device_id %d", d.DeviceID),
		fmt.Sprintf("location_id %d", locationID),
		fmt.Sprintf("domain %d", domain),
		fmt.Sprintf("drm_render_minor %d", dev.DRMRenderMinor),
		fmt.Sprintf("hive_id %d", hiveID),
		fmt.Sprintf("num_sdma_engines %d", c.NumSDMAEngines),
		fmt.Sprintf("num_sdma_xgmi_engines %d", c.NumSDMAXGMIEngines),
		"num_sdma_queues_per_engine 4",
		fmt.Sprintf("num_cp_queues %d", c.NumCPQueues),
		fmt.Sprintf("max_engine_clk_fcompute %d", d.Clocks.MaxGFXClkMHz),
		fmt.Sprintf("local_mem_size %d", d.Memory.VRAMSizeBytes),
		"fw_version 0",
		"capability 0x00002b3a",
		"debug_prop 0x0000000000000900",
		"sdma_fw_version 0",
		fmt.Sprintf("unique_id %d", uniqueID),
		fmt.Sprintf("num_xcc %d", c.NumXCC),
	}

	if err := writeFile(filepath.Join(dir, "gpu_id"), fmt.Sprintf("%d\n", gpuID)); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, "name"), d.Name+"\n"); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, "properties"), strings.Join(props, "\n")+"\n"); err != nil {
		return err
	}

	// mem_banks/0/properties
	memProps := []string{
		"heap_type 1",
		fmt.Sprintf("size_in_bytes %d", d.Memory.VRAMSizeBytes),
		"flags 0",
		fmt.Sprintf("width %d", d.Memory.VRAMBusWidth),
		fmt.Sprintf("mem_clk_max %d", d.Memory.MaxMemClkMHz),
	}
	if err := writeFile(filepath.Join(dir, "mem_banks/0/properties"),
		strings.Join(memProps, "\n")+"\n"); err != nil {
		return err
	}

	// io_links: one PCIe link to CPU, then xGMI links to peer GPUs
	linkIdx := 0
	pcieLinkProps := []string{
		fmt.Sprintf("type %d", 2), // PCIe
		"version_major 0",
		"version_minor 0",
		fmt.Sprintf("node_from %d", dev.Index+1),
		"node_to 0",
		"weight 40",
		"min_latency 0",
		"max_latency 0",
		"min_bandwidth 0",
		"max_bandwidth 0",
		"recommended_transfer_size 0",
		"flags 0x0000000000000001",
	}
	if err := writeFile(filepath.Join(dir, fmt.Sprintf("io_links/%d/properties", linkIdx)),
		strings.Join(pcieLinkProps, "\n")+"\n"); err != nil {
		return err
	}
	linkIdx++

	// xGMI links to other GPUs
	for _, peer := range r.profile.Devices {
		if peer.PCIBDF == dev.PCIBDF {
			continue
		}
		xgmiProps := []string{
			fmt.Sprintf("type %d", r.profile.XGMI.LinkType), // xGMI
			"version_major 0",
			"version_minor 0",
			fmt.Sprintf("node_from %d", dev.Index+1),
			fmt.Sprintf("node_to %d", peer.Index+1),
			"weight 15",
			"min_latency 0",
			"max_latency 0",
			"min_bandwidth 0",
			fmt.Sprintf("max_bandwidth %d", r.profile.XGMI.BandwidthPerLink*1000), // GB/s -> MB/s
			"recommended_transfer_size 0",
			"flags 0x0000000000000001",
		}
		if err := writeFile(filepath.Join(dir, fmt.Sprintf("io_links/%d/properties", linkIdx)),
			strings.Join(xgmiProps, "\n")+"\n"); err != nil {
			return err
		}
		linkIdx++
	}

	// NUMA node in DRM path
	drmDir := filepath.Join(r.rootDir, fmt.Sprintf("sys/class/drm/card%d/device", dev.Index))
	if err := writeFile(filepath.Join(drmDir, "numa_node"), fmt.Sprintf("%d\n", numaNode)); err != nil {
		return err
	}

	return nil
}

// renderDriverBDF restores the driver sysfs directory for a single GPU.
// Called when recovering a crashed GPU.
func (r *Renderer) renderDriverBDF(gpu *GPUState) {
	if gpu.PartitionIndex > 0 {
		dir := filepath.Join(r.rootDir, fmt.Sprintf("sys/devices/platform/amdgpu_xcp_%d/drm", gpu.Index))
		for _, name := range []string{fmt.Sprintf("card%d", gpu.Index), fmt.Sprintf("renderD%d", r.renderMinor(gpu.Index))} {
			writeFile(filepath.Join(dir, name, "device/vendor"), fmt.Sprintf("0x%04x\n", r.profile.DeviceDefault.VendorID))
		}
		return
	}

	moduleDir := filepath.Join(r.rootDir, "sys/module/amdgpu")
	driverDir := filepath.Join(moduleDir, "drivers/pci:amdgpu")
	bdfDir := filepath.Join(driverDir, gpu.PCIBDF)
	numaNode := 0
	for _, rc := range r.profile.PCIETopology.RootComplexes {
		for _, d := range rc.Devices {
			if d == gpu.PCIBDF {
				numaNode = rc.NUMANode
			}
		}
	}
	writeFile(filepath.Join(bdfDir, "numa_node"), fmt.Sprintf("%d\n", numaNode))
	writeFile(filepath.Join(bdfDir, "current_compute_partition"), gpu.Partition+"\n")
	writeFile(filepath.Join(bdfDir, "current_memory_partition"), r.profile.DeviceDefault.Partition.NPSMode+"\n")
	writeFile(filepath.Join(bdfDir, "vendor"), fmt.Sprintf("0x%04x\n", r.profile.DeviceDefault.VendorID))
	writeFile(filepath.Join(bdfDir, "device"), fmt.Sprintf("0x%04x\n", r.profile.DeviceDefault.DeviceID))
	cardDir := filepath.Join(bdfDir, fmt.Sprintf("drm/card%d/device", gpu.Index))
	writeFile(filepath.Join(cardDir, "vendor"), fmt.Sprintf("0x%04x\n", r.profile.DeviceDefault.VendorID))
	renderDir := filepath.Join(bdfDir, fmt.Sprintf("drm/renderD%d/device", r.renderMinor(gpu.Index)))
	writeFile(filepath.Join(renderDir, "vendor"), fmt.Sprintf("0x%04x\n", r.profile.DeviceDefault.VendorID))
}

func (r *Renderer) renderDRMDevices() error {
	for _, dev := range r.profile.Devices {
		drmDir := filepath.Join(r.rootDir, fmt.Sprintf("sys/class/drm/card%d/device", dev.Index))

		if err := writeFile(filepath.Join(drmDir, "vendor"), fmt.Sprintf("0x%04x\n", r.profile.DeviceDefault.VendorID)); err != nil {
			return err
		}
		if err := writeFile(filepath.Join(drmDir, "device"), fmt.Sprintf("0x%04x\n", r.profile.DeviceDefault.DeviceID)); err != nil {
			return err
		}
		// Read by AMD's DRA driver (productName attribute) and node labeller
		// (product-name label); without it both come back empty on the mock.
		if err := writeFile(filepath.Join(drmDir, "product_name"), r.profile.DeviceDefault.SysfsProductName()+"\n"); err != nil {
			return err
		}
		if err := writeFile(filepath.Join(drmDir, "uevent"),
			fmt.Sprintf("DRIVER=amdgpu\nPCI_CLASS=38000\nPCI_ID=1002:%04X\nPCI_SUBSYS_ID=1002:%04X\nPCI_SLOT_NAME=%s\n",
				r.profile.DeviceDefault.DeviceID,
				r.profile.DeviceDefault.SubsystemDeviceID,
				dev.PCIBDF)); err != nil {
			return err
		}

		if err := r.renderDeviceAttrs(drmDir, &dev); err != nil {
			return err
		}
		if r.profile.XGMI.HiveID != "" {
			xgmiDir := filepath.Join(drmDir, "xgmi_hive_info")
			if err := writeFile(filepath.Join(xgmiDir, "xgmi_hive_id"), r.profile.XGMI.HiveID+"\n"); err != nil {
				return err
			}
			if err := writeFile(filepath.Join(drmDir, "xgmi_device_id"), dev.XGMIDeviceID+"\n"); err != nil {
				return err
			}
			if err := writeFile(filepath.Join(drmDir, "xgmi_error"), "0\n"); err != nil {
				return err
			}
		}
		if err := writeFile(filepath.Join(drmDir, "ras/gpu_vram_bad_pages"), ""); err != nil {
			return err
		}
	}
	return nil
}

func (r *Renderer) renderDeviceNodes() error {
	if r.backend != nil {
		return r.backend.Reconcile(r.profile)
	}
	devDir := filepath.Join(r.rootDir, "dev")

	// /dev/kfd: KFD compute interface (one per system)
	kfdPath := filepath.Join(devDir, "kfd")
	if err := os.MkdirAll(filepath.Dir(kfdPath), 0o755); err != nil {
		return err
	}
	// Create a regular file as placeholder; real mknod requires privileges
	// and is done by the entrypoint script.
	if err := ensureDevicePlaceholder(kfdPath); err != nil {
		return err
	}

	// /dev/dri/renderDN: one per GPU
	driDir := filepath.Join(devDir, "dri")
	for _, dev := range r.profile.Devices {
		renderPath := filepath.Join(driDir, fmt.Sprintf("renderD%d", dev.DRMRenderMinor))
		if err := ensureDevicePlaceholder(renderPath); err != nil {
			return err
		}
	}

	return nil
}

func (r *Renderer) renderDriverModule() error {
	if err := r.renderPartitionDevices(); err != nil {
		return err
	}
	moduleDir := filepath.Join(r.rootDir, "sys/module/amdgpu")

	if err := writeFile(filepath.Join(moduleDir, "refcnt"), "1\n"); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(moduleDir, "version"), r.profile.System.AMDGPUVersion+"\n"); err != nil {
		return err
	}
	// amd-smi CLI checks this file to determine if the driver is loaded.
	// Must contain "live" for amd-smi to proceed with initialization.
	if err := writeFile(filepath.Join(moduleDir, "initstate"), "live\n"); err != nil {
		return err
	}

	// The AMD device plugin enumerates GPUs by walking:
	//   /sys/module/amdgpu/drivers/pci:amdgpu/<BDF>/drm/cardN/device/vendor
	// Each BDF directory must contain drm/cardN with a device/vendor file
	// reading 0x1002 for the plugin to recognize it as an AMD GPU.
	driverDir := filepath.Join(moduleDir, "drivers/pci:amdgpu")
	for _, dev := range r.profile.Devices {
		if dev.PartitionIndex > 0 {
			continue
		}
		bdfDir := filepath.Join(driverDir, dev.PCIBDF)
		numaNode := r.numaForBDF(dev.PCIBDF)

		// Files the device plugin reads directly from the BDF directory
		if err := writeFile(filepath.Join(bdfDir, "numa_node"),
			fmt.Sprintf("%d\n", numaNode)); err != nil {
			return err
		}
		if err := writeFile(filepath.Join(bdfDir, "current_compute_partition"),
			r.profile.DeviceDefault.Partition.Mode+"\n"); err != nil {
			return err
		}
		if err := writeFile(filepath.Join(bdfDir, "current_memory_partition"),
			r.profile.DeviceDefault.Partition.NPSMode+"\n"); err != nil {
			return err
		}
		if err := writeFile(filepath.Join(bdfDir, "vendor"),
			fmt.Sprintf("0x%04x\n", r.profile.DeviceDefault.VendorID)); err != nil {
			return err
		}
		if err := writeFile(filepath.Join(bdfDir, "device"),
			fmt.Sprintf("0x%04x\n", r.profile.DeviceDefault.DeviceID)); err != nil {
			return err
		}
		if err := writeFile(filepath.Join(bdfDir, "uevent"),
			fmt.Sprintf("DRIVER=amdgpu\nPCI_CLASS=038000\nPCI_ID=1002:%04X\nPCI_SUBSYS_ID=1002:%04X\nPCI_SLOT_NAME=%s\n",
				r.profile.DeviceDefault.DeviceID,
				r.profile.DeviceDefault.SubsystemDeviceID,
				dev.PCIBDF)); err != nil {
			return err
		}

		// drm/cardN and drm/renderDN inside the BDF directory
		cardDir := filepath.Join(bdfDir, fmt.Sprintf("drm/card%d", dev.Index))
		if err := writeFile(filepath.Join(cardDir, "device/vendor"),
			fmt.Sprintf("0x%04x\n", r.profile.DeviceDefault.VendorID)); err != nil {
			return err
		}

		renderDir := filepath.Join(bdfDir, fmt.Sprintf("drm/renderD%d", dev.DRMRenderMinor))
		if err := writeFile(filepath.Join(renderDir, "device/vendor"),
			fmt.Sprintf("0x%04x\n", r.profile.DeviceDefault.VendorID)); err != nil {
			return err
		}
	}
	return nil
}

func (r *Renderer) renderPCISysfs() error {
	for _, dev := range r.profile.Devices {
		numaNode := r.numaForBDF(dev.PCIBDF)

		for _, rc := range r.profile.PCIETopology.RootComplexes {
			for _, rcDev := range rc.Devices {
				if rcDev == dev.PCIBDF {
					devDir := filepath.Join(r.rootDir, "sys/bus/pci/devices", dev.PCIBDF)
					devicesDir := filepath.Join(r.rootDir, "sys/devices", rc.ID, dev.PCIBDF)

					if err := writeFile(filepath.Join(devicesDir, "vendor"), fmt.Sprintf("0x%04x\n", r.profile.DeviceDefault.VendorID)); err != nil {
						return err
					}
					if err := writeFile(filepath.Join(devicesDir, "device"), fmt.Sprintf("0x%04x\n", r.profile.DeviceDefault.DeviceID)); err != nil {
						return err
					}
					if err := writeFile(filepath.Join(devicesDir, "class"), "0x038000\n"); err != nil {
						return err
					}
					if err := writeFile(filepath.Join(devicesDir, "numa_node"), fmt.Sprintf("%d\n", numaNode)); err != nil {
						return err
					}

					// symlink from bus to devices
					if err := os.MkdirAll(filepath.Dir(devDir), 0o755); err != nil {
						return err
					}
					rel, _ := filepath.Rel(filepath.Dir(devDir), devicesDir)
					_ = os.Remove(devDir)
					if err := os.Symlink(rel, devDir); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// renderDriverLinks adds the driver symlinks a real amdgpu system has:
//
//	/sys/bus/pci/drivers/amdgpu/module  -> /sys/module/amdgpu
//	/sys/bus/pci/drivers/amdgpu/<BDF>   -> the PCI device
//	/sys/devices/<root>/<BDF>/driver    -> /sys/bus/pci/drivers/amdgpu
//	/sys/class/drm/cardN/device/driver  -> /sys/bus/pci/drivers/amdgpu
//
// AMD's DRA driver v1.0.0 and node labeller read the driver version through
// /sys/class/drm/cardN/device/driver/module/version. Without these links the
// DRA driver publishes an empty driverVersion, which is not valid semver, so
// the API server rejects its ResourceSlice and no GPUs are advertised.
func (r *Renderer) renderDriverLinks() error {
	sysDir := filepath.Join(r.rootDir, "sys")
	drvDir := filepath.Join(sysDir, "bus/pci/drivers/amdgpu")
	if err := os.MkdirAll(drvDir, 0o755); err != nil {
		return err
	}
	if err := relSymlink(filepath.Join(sysDir, "module/amdgpu"), filepath.Join(drvDir, "module")); err != nil {
		return err
	}
	for _, dev := range r.profile.Devices {
		cardDriver := filepath.Join(sysDir, fmt.Sprintf("class/drm/card%d/device/driver", dev.Index))
		if err := relSymlink(drvDir, cardDriver); err != nil {
			return err
		}
		for _, rc := range r.profile.PCIETopology.RootComplexes {
			for _, bdf := range rc.Devices {
				if bdf != dev.PCIBDF {
					continue
				}
				pciDev := filepath.Join(sysDir, "devices", rc.ID, bdf)
				if err := relSymlink(drvDir, filepath.Join(pciDev, "driver")); err != nil {
					return err
				}
				if err := relSymlink(pciDev, filepath.Join(drvDir, bdf)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// relSymlink creates or replaces link so it points at target through a
// relative path, as sysfs does, so the tree resolves wherever it is mounted.
func relSymlink(target, link string) error {
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	rel, err := filepath.Rel(filepath.Dir(link), target)
	if err != nil {
		return err
	}
	_ = os.Remove(link)
	return os.Symlink(rel, link)
}

func (r *Renderer) ioLinkCount(dev *DeviceConfig) int {
	// Sibling compute partitions are not separate physical xGMI peers.
	count := 1
	for _, peer := range r.profile.Devices {
		if peer.PCIBDF != dev.PCIBDF {
			count++
		}
	}
	return count
}

func (r *Renderer) numaForBDF(bdf string) int {
	for _, rc := range r.profile.PCIETopology.RootComplexes {
		for _, d := range rc.Devices {
			if d == bdf {
				return rc.NUMANode
			}
		}
	}
	return -1
}

func bdfToLocationID(bdf string) uint32 {
	// Parse "DDDD:BB:DD.F" to a location_id
	var domain, bus, device, fn uint32
	fmt.Sscanf(bdf, "%x:%x:%x.%x", &domain, &bus, &device, &fn)
	return (bus << 8) | (device << 3) | fn
}

func bdfToDomain(bdf string) uint32 {
	var domain uint32
	fmt.Sscanf(bdf, "%x:", &domain)
	return domain
}

// Cleanup removes all rendered surfaces.
func (r *Renderer) Cleanup() error {
	return os.RemoveAll(r.rootDir)
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// Existing mock character devices survive an unclean agent shutdown. Never open
// them for writing: without a backing GPU driver, open returns ENXIO.
func ensureDevicePlaceholder(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeCharDevice != 0 {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unexpected device placeholder type at %s: %s", path, info.Mode())
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return writeFile(path, "")
}

// clearDirectory preserves the directory inode used by hostPath bind mounts.
func clearDirectory(path string) error {
	if err := os.MkdirAll(path, 0755); err != nil {
		return err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(path, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// setXGMILink marks every xGMI io_link touching a KFD node as disabled or
// enabled. A link is up only while both of its ends are up, so recovering one
// GPU leaves its links to a still-faulted peer down.
func (r *Renderer) setXGMILink(card, node int, down bool) error {
	if r.profile.XGMI.HiveID == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.xgmiDown[node] == down {
		return nil
	}
	if r.xgmiDown == nil {
		r.xgmiDown = map[int]bool{}
	}
	r.xgmiDown[node] = down
	errFlag := "0\n"
	if down {
		errFlag = "1\n"
	}
	if err := writeFile(filepath.Join(r.rootDir, fmt.Sprintf("sys/class/drm/card%d/device/xgmi_error", card)), errFlag); err != nil {
		return err
	}
	links, err := filepath.Glob(filepath.Join(r.rootDir, "sys/class/kfd/kfd/topology/nodes/*/io_links/*/properties"))
	if err != nil {
		return err
	}
	for _, path := range links {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(data), "\n")
		prop := map[string]int{}
		flagLine := -1
		for i, line := range lines {
			f := strings.Fields(line)
			if len(f) != 2 {
				continue
			}
			if f[0] == "flags" {
				flagLine = i
			} else if v, err := strconv.Atoi(f[1]); err == nil {
				prop[f[0]] = v
			}
		}
		if flagLine < 0 || prop["type"] != int(r.profile.XGMI.LinkType) {
			continue
		}
		enabled := 1
		if r.xgmiDown[prop["node_from"]] || r.xgmiDown[prop["node_to"]] {
			enabled = 0
		}
		lines[flagLine] = fmt.Sprintf("flags 0x%016x", enabled)
		if err := writeFile(path, strings.Join(lines, "\n")); err != nil {
			return err
		}
	}
	return nil
}

// parseID reads a profile ID written as decimal or 0x-prefixed hex.
func parseID(s string) (uint64, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.ParseUint(s, 0, 64)
}

// renderDeviceAttrs writes the amdgpu device attributes that do not change
// at runtime, plus the starting value of those that do.
func (r *Renderer) renderDeviceAttrs(drmDir string, dev *DeviceConfig) error {
	d := r.profile.DeviceDefault
	uniqueID, err := parseID(dev.UniqueID)
	if err != nil {
		return fmt.Errorf("invalid unique_id for GPU %d: %w", dev.Index, err)
	}
	const microwatt, millidegree = 1_000_000, 1000
	files := map[string]string{
		"subsystem_vendor":        fmt.Sprintf("0x%04x", d.SubsystemVendorID),
		"subsystem_device":        fmt.Sprintf("0x%04x", d.SubsystemDeviceID),
		"unique_id":               fmt.Sprintf("%016x", uniqueID),
		"mem_info_vram_total":     fmt.Sprint(d.Memory.VRAMSizeBytes),
		"mem_info_vis_vram_total": fmt.Sprint(d.Memory.VisVRAMBytes),
		"mem_info_gtt_total":      fmt.Sprint(d.Memory.GTTSizeBytes),
		"mem_info_vram_used":      fmt.Sprint(d.Memory.VRAMUsedBytes),
		"current_link_speed":      linkSpeed(d.PCIe.Gen),
		"current_link_width":      fmt.Sprint(d.PCIe.Width),
		"max_link_speed":          linkSpeed(d.PCIe.MaxGen),
		"max_link_width":          fmt.Sprint(d.PCIe.MaxWidth),
		"gpu_busy_percent":        fmt.Sprint(d.Utilization.GFXActivityPercent),
		"mem_busy_percent":        fmt.Sprint(d.Utilization.UMCActivityPercent),
		"pp_dpm_sclk":             dpmTable(d.Clocks.MinGFXClkMHz, d.Clocks.MaxGFXClkMHz, d.Clocks.CurrentGFXClkMHz),
		"pp_dpm_mclk":             dpmTable(d.Clocks.MinMemClkMHz, d.Clocks.MaxMemClkMHz, d.Clocks.CurrentMemClkMHz),

		"mem_info_vis_vram_used":            fmt.Sprint(d.Memory.VRAMUsedBytes),
		"mem_info_gtt_used":                 "0",
		"power_dpm_force_performance_level": "auto",
		"thermal_throttling_logging":        dev.PCIBDF + ": thermal throttling logging enabled, with interval 60 seconds",

		// Labels as the amdgpu hwmon device reports them.
		"hwmon/hwmon0/temp1_label":  "edge",
		"hwmon/hwmon0/temp2_label":  "junction",
		"hwmon/hwmon0/temp3_label":  "mem",
		"hwmon/hwmon0/power1_label": "PPT",
		"hwmon/hwmon0/freq1_label":  "sclk",
		"hwmon/hwmon0/freq2_label":  "mclk",
		"hwmon/hwmon0/freq2_input":  fmt.Sprint(uint64(d.Clocks.CurrentMemClkMHz) * 1_000_000),
		"hwmon/hwmon0/temp2_crit":   fmt.Sprint(d.Thermal.SlowdownTempC * millidegree),
		"hwmon/hwmon0/temp3_crit":   fmt.Sprint(d.Thermal.SlowdownTempC * millidegree),

		"hwmon/hwmon0/name":               "amdgpu",
		"hwmon/hwmon0/power1_cap":         fmt.Sprint(uint64(d.Power.DefaultPowerCapW) * microwatt),
		"hwmon/hwmon0/power1_cap_default": fmt.Sprint(uint64(d.Power.DefaultPowerCapW) * microwatt),
		"hwmon/hwmon0/power1_cap_max":     fmt.Sprint(uint64(d.Power.MaxPowerCapW) * microwatt),
		"hwmon/hwmon0/power1_cap_min":     fmt.Sprint(uint64(d.Power.MinPowerCapW) * microwatt),
		"hwmon/hwmon0/power1_average":     fmt.Sprint(uint64(d.Power.CurrentSocketPowerW) * microwatt),
		"hwmon/hwmon0/temp1_input":        fmt.Sprint(d.Thermal.EdgeTempC * millidegree),
		"hwmon/hwmon0/temp2_input":        fmt.Sprint(d.Thermal.HotspotTempC * millidegree),
		"hwmon/hwmon0/temp3_input":        fmt.Sprint(d.Thermal.MemTempC * millidegree),
		"hwmon/hwmon0/temp1_crit":         fmt.Sprint(d.Thermal.SlowdownTempC * millidegree),
		"hwmon/hwmon0/temp1_emergency":    fmt.Sprint(d.Thermal.ShutdownTempC * millidegree),
		"hwmon/hwmon0/freq1_input":        fmt.Sprint(uint64(d.Clocks.CurrentGFXClkMHz) * 1_000_000),
	}
	for name, value := range files {
		if err := writeFile(filepath.Join(drmDir, name), value+"\n"); err != nil {
			return err
		}
	}
	return nil
}

// WriteTelemetrySysfs refreshes the live sensor attributes of one GPU. It is
// cheap enough for every simulator tick and replaces files atomically, so a
// reader never sees a half-written value.
func (r *Renderer) WriteTelemetrySysfs(gpu *GPUState) error {
	dir := filepath.Join(r.rootDir, fmt.Sprintf("sys/class/drm/card%d/device", gpu.Index))
	if _, err := os.Stat(dir); err != nil {
		return nil // display-only virtual partitions have no rendered card
	}
	clocks := r.profile.DeviceDefault.Clocks
	// Hotspot and memory offsets match the mock libamd_smi.so.
	files := map[string]string{
		"gpu_busy_percent":            fmt.Sprint(gpu.UtilizationPct),
		"mem_info_vram_used":          fmt.Sprint(uint64(gpu.MemoryUsedMB) << 20),
		"mem_info_vis_vram_used":      fmt.Sprint(uint64(gpu.MemoryUsedMB) << 20),
		"hwmon/hwmon0/freq2_input":    fmt.Sprint(uint64(gpu.ClockMemMHz) * 1_000_000),
		"pp_dpm_sclk":                 dpmTable(clocks.MinGFXClkMHz, clocks.MaxGFXClkMHz, uint32(gpu.ClockGFXMHz)),
		"hwmon/hwmon0/temp1_input":    fmt.Sprint(gpu.TemperatureC * 1000),
		"hwmon/hwmon0/temp2_input":    fmt.Sprint(max(gpu.TemperatureC+6, 0) * 1000),
		"hwmon/hwmon0/temp3_input":    fmt.Sprint(max(gpu.TemperatureC-4, 0) * 1000),
		"hwmon/hwmon0/power1_average": fmt.Sprint(uint64(gpu.PowerW) * 1_000_000),
		"hwmon/hwmon0/freq1_input":    fmt.Sprint(uint64(gpu.ClockGFXMHz) * 1_000_000),
	}
	for name, value := range files {
		path := filepath.Join(dir, name)
		tmp := path + ".tmp"
		if err := writeFile(tmp, value+"\n"); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
	}
	return nil
}

// linkSpeed formats a PCIe generation the way sysfs reports link speed.
func linkSpeed(gen uint32) string {
	speeds := []string{"Unknown", "2.5 GT/s PCIe", "5.0 GT/s PCIe", "8.0 GT/s PCIe", "16.0 GT/s PCIe", "32.0 GT/s PCIe", "64.0 GT/s PCIe"}
	if int(gen) >= len(speeds) {
		return speeds[0]
	}
	return speeds[gen]
}

// dpmTable renders a pp_dpm_* level table; the star marks the level in use.
func dpmTable(lo, hi, current uint32) string {
	if lo == 0 || lo >= hi {
		return fmt.Sprintf("0: %dMhz *", max(lo, hi))
	}
	if 2*uint64(current) >= uint64(lo)+uint64(hi) {
		return fmt.Sprintf("0: %dMhz\n1: %dMhz *", lo, hi)
	}
	return fmt.Sprintf("0: %dMhz *\n1: %dMhz", lo, hi)
}

// renderMinor returns the DRM render minor of a card. Profiles state it per
// device; 128+index is only the layout of a machine whose cards were probed
// in order with nothing else on the DRM bus, so it is the fallback, not the rule.
func (r *Renderer) renderMinor(index int) int {
	for _, dev := range r.profile.Devices {
		if dev.Index == index {
			return dev.DRMRenderMinor
		}
	}
	return 128 + index
}
