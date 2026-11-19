package sysfs

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/mesutoezdil/fake-gpu/pkg/fleet"
	"github.com/mesutoezdil/fake-gpu/pkg/profile"
)

func catalog(t *testing.T) *profile.Catalog {
	t.Helper()
	c, err := profile.LoadDir("../../profiles")
	if err != nil || len(c.Slugs) != 7 {
		t.Fatalf("%v %v", err, c)
	}
	return c
}

func rendered(t *testing.T, p *profile.Profile) (*Tree, func(string) string) {
	t.Helper()
	tree := New(p, t.TempDir())
	if err := tree.Render(); err != nil {
		t.Fatal(err)
	}
	return tree, func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(tree.Root(), rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
}

func exists(t *testing.T, tree *Tree, rel string) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(tree.Root(), rel))
	return err == nil
}

func props(text string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			m[f[0]] = f[1]
		}
	}
	return m
}

func TestKFDTopology(t *testing.T) {
	for slug, p := range catalog(t).Profiles {
		t.Run(slug, func(t *testing.T) {
			tree, read := rendered(t, p)
			c := p.Card
			if cpu := props(read(node(0) + "/properties")); cpu["cpu_cores_count"] != "128" || cpu["simd_count"] != "0" || len(cpu) != 37 {
				t.Fatalf("CPU node %v", cpu)
			}
			for pos, d := range p.Devices {
				dir := node(pos + 1)
				if read(dir+"/name") != c.Name+"\n" || read(dir+"/gpu_id") != fmt.Sprintf("%d\n", d.Index+1) {
					t.Fatal("node identity")
				}
				k := props(read(dir + "/properties"))
				bdf := profile.ParseBDF(d.BDF)
				for key, want := range map[string]uint64{
					"simd_count": uint64(c.Compute.SIMDs), "gfx_target_version": uint64(c.GFXTarget), "vendor_id": uint64(c.Vendor),
					"device_id": uint64(c.Device), "drm_render_minor": uint64(d.RenderMinor), "local_mem_size": c.Memory.VRAM,
					"location_id": uint64(bdf.Location()), "domain": uint64(bdf.Domain), "io_links_count": uint64(len(p.Devices)),
				} {
					if k[key] != strconv.FormatUint(want, 10) {
						t.Errorf("GPU %d %s = %s, want %d", d.Index, key, k[key], want)
					}
				}
				if bank := props(read(dir + "/mem_banks/0/properties")); bank["size_in_bytes"] != strconv.FormatUint(c.Memory.VRAM, 10) {
					t.Fatal("memory bank")
				}
				if pcie := props(read(dir + "/io_links/0/properties")); pcie["type"] != "2" || pcie["node_to"] != "0" {
					t.Fatal("link 0 must be PCIe to the CPU")
				}
				if exists(t, tree, fmt.Sprintf("%s/io_links/%d", dir, len(p.Devices))) {
					t.Fatal("one link too many")
				}
			}
		})
	}
}

// KFD prints IDs in decimal; a decimal-only reader would see 0x... as 0.
func TestUniqueIDIsDecimal(t *testing.T) {
	base := catalog(t).Profiles["mi300x"]
	for in, want := range map[string]string{
		"0x9c4f000000000001": "11263221193076899841", "11263221293014450177": "11263221293014450177",
		"18446744073709551615": "18446744073709551615", "0": "0", "": "0",
	} {
		p := *base
		p.Devices = append([]profile.Device(nil), base.Devices...)
		p.Devices[0].UniqueID = in
		_, read := rendered(t, &p)
		if got := props(read(node(1) + "/properties"))["unique_id"]; got != want {
			t.Errorf("%q: unique_id %s, want %s", in, got, want)
		}
	}
	p := *base
	p.Devices = append([]profile.Device(nil), base.Devices...)
	p.Devices[0].UniqueID = "not-a-number"
	if err := New(&p, t.TempDir()).Render(); err == nil {
		t.Fatal("invalid unique ID accepted")
	}
	p = *base
	p.XGMI.Hive = "hive"
	if err := New(&p, t.TempDir()).Render(); err == nil {
		t.Fatal("invalid hive ID accepted")
	}
}

// AMD's DRA driver and node labeller read these paths, through links that
// must resolve wherever the tree is mounted and after a profile switch.
func TestDiscoveryPaths(t *testing.T) {
	c := catalog(t)
	tree := New(c.Profiles[c.Slugs[0]], t.TempDir())
	if err := tree.Render(); err != nil {
		t.Fatal(err)
	}
	for _, slug := range c.Slugs {
		p := c.Profiles[slug]
		if err := tree.Switch(p); err != nil {
			t.Fatal(err)
		}
		read := func(rel string) string {
			b, err := os.ReadFile(filepath.Join(tree.Root(), rel))
			if err != nil {
				t.Fatalf("%s: %v", slug, err)
			}
			return string(b)
		}
		for _, d := range p.Devices {
			cardDir := card(d.Index)
			if read(cardDir+"/driver/module/version") != p.Software.Driver+"\n" || read(cardDir+"/product_name") != p.Card.ProductName()+"\n" {
				t.Fatalf("%s card %d", slug, d.Index)
			}
			if read("sys/bus/pci/devices/"+d.BDF+"/driver/module/version") != p.Software.Driver+"\n" {
				t.Fatalf("%s: PCI driver path", slug)
			}
			if read(bound+"/"+d.BDF+fmt.Sprintf("/drm/card%d/device/vendor", d.Index)) != "0x1002\n" {
				t.Fatalf("%s: device plugin path", slug)
			}
			if !strings.Contains(read(cardDir+"/uevent"), "PCI_SLOT_NAME="+d.BDF+"\n") {
				t.Fatal("uevent")
			}
		}
		for _, rel := range []string{"sys/class/dmi/id/product_uuid", "sys/devices/virtual/dmi/id/product_uuid"} {
			if len(strings.TrimSpace(read(rel))) != 36 {
				t.Fatalf("%s: kind mounts a UUID over %s", slug, rel)
			}
		}
		if read(module+"/initstate") != "live\n" {
			t.Fatal("amd-smi needs initstate live")
		}
	}
}

// Consumers hold bind mounts to sys/ and smi/ across a rolling update, so
// re-rendering keeps those inodes and drops what the new profile lacks.
func TestRerenderKeepsMountedDirectories(t *testing.T) {
	c := catalog(t)
	tree := New(c.Profiles["mi250x"], t.TempDir())
	if err := tree.Render(); err != nil {
		t.Fatal(err)
	}
	stat := func(rel string) os.FileInfo {
		info, err := os.Stat(filepath.Join(tree.Root(), rel))
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	sys, smi := stat("sys"), stat("smi")
	if err := New(c.Profiles["mi300a"], tree.Root()).Render(); err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(sys, stat("sys")) || !os.SameFile(smi, stat("smi")) {
		t.Fatal("a mounted directory was replaced")
	}
	nodes, _ := os.ReadDir(filepath.Join(tree.Root(), topology, "nodes"))
	if len(nodes) != 5 || exists(t, tree, "smi/gpu15") || exists(t, tree, card(15)) {
		t.Fatalf("stale inventory: %d KFD nodes", len(nodes))
	}
}

func TestDevicePlaceholders(t *testing.T) {
	tree, read := rendered(t, catalog(t).Profiles["mi300x"])
	for _, rel := range []string{"dev/kfd", "dev/dri/renderD128", "dev/dri/renderD135"} {
		if read(rel) != "" {
			t.Fatal(rel)
		}
	}
	os.Remove(filepath.Join(tree.Root(), "dev/kfd"))
	os.Mkdir(filepath.Join(tree.Root(), "dev/kfd"), 0o755)
	if err := tree.Render(); err == nil {
		t.Fatal("a directory where /dev/kfd belongs was accepted")
	}
}

type recorder struct {
	reconciled        int
	removed, restored []int
}

func (r *recorder) Reconcile(*profile.Profile) error { r.reconciled++; return nil }
func (r *recorder) RemoveDevice(i int) error         { r.removed = append(r.removed, i); return nil }
func (r *recorder) RestoreDevice(i int) error        { r.restored = append(r.restored, i); return nil }

func TestBackend(t *testing.T) {
	p := catalog(t).Profiles["mi300x"]
	b := &recorder{}
	tree := New(p, t.TempDir())
	tree.SetBackend(b)
	if err := tree.Render(); err != nil {
		t.Fatal(err)
	}
	if exists(t, tree, "dev/kfd") || b.reconciled != 1 {
		t.Fatal("with a backend the tree must not make placeholders")
	}
	if err := tree.Switch(p); err != nil || b.reconciled != 2 {
		t.Fatal("a profile switch must reconcile the backend")
	}
	g := fleet.FromProfile(p, "mi300x")[0]
	g.Status = fleet.Crashed
	tree.Apply(&g)
	g.Status = fleet.Healthy
	tree.Apply(&g)
	if fmt.Sprint(b.removed, b.restored) != "[0] [0]" {
		t.Fatalf("crash and recovery bypassed the backend: %v %v", b.removed, b.restored)
	}
}

func TestSchedulablePartitions(t *testing.T) {
	spx := catalog(t).Profiles["mi300x"]
	p, err := profile.Partition(spx, "DPX")
	if err != nil {
		t.Fatal(err)
	}
	tree, read := rendered(t, p)
	for _, d := range p.Devices {
		render := fmt.Sprintf("/drm/renderD%d/device/vendor", d.RenderMinor)
		parent := bound + "/" + d.BDF
		if d.Slice == 0 {
			if read(parent+render) != "0x1002\n" || read(parent+"/current_memory_partition") != "NPS2\n" || read(parent+"/current_compute_partition") != "DPX\n" {
				t.Fatal("first slice is the PCI device")
			}
		} else {
			if read(xcp(d.Index)+render) != "0x1002\n" || exists(t, tree, parent+fmt.Sprintf("/drm/card%d", d.Index)) {
				t.Fatal("second slice is an amdgpu_xcp platform device, not another PCI function")
			}
		}
		// Slices of one card share no xGMI link.
		if n := props(read(node(d.Index+1) + "/properties"))["io_links_count"]; n != "15" {
			t.Fatalf("io_links_count %s", n)
		}
	}
}

func TestDPXRecoveryKeepsTheSibling(t *testing.T) {
	p, err := profile.Partition(catalog(t).Profiles["mi300x"], "DPX")
	if err != nil {
		t.Fatal(err)
	}
	tree, read := rendered(t, p)
	gpus := fleet.FromProfile(p, "mi300x")
	for _, i := range []int{1, 0} {
		g := gpus[i]
		g.Status = fleet.Crashed
		tree.Apply(&g)
		if exists(t, tree, tree.binding(p.Devices[i])) {
			t.Fatalf("slice %d still bound", i)
		}
		if i == 0 && !exists(t, tree, xcp(1)) {
			t.Fatal("crashing the first slice unbound the second")
		}
		g.Status = fleet.Healthy
		tree.Apply(&g)
	}
	parent := bound + "/" + p.Devices[0].BDF
	if read(parent+"/drm/renderD128/device/vendor") != "0x1002\n" || read(xcp(1)+"/drm/renderD129/device/vendor") != "0x1002\n" || read(parent+"/current_memory_partition") != "NPS2\n" {
		t.Fatal("recovery did not restore both slices")
	}
	if exists(t, tree, parent+"/drm/renderD129") {
		t.Fatal("recovery made the second slice a PCI device")
	}
}

func apply(t *testing.T, tree *Tree, g fleet.GPU, change func(*fleet.GPU)) fleet.GPU {
	t.Helper()
	change(&g)
	if err := tree.Apply(&g); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestFaultsReachTheTree(t *testing.T) {
	p := catalog(t).Profiles["mi300x"]
	tree, read := rendered(t, p)
	g := fleet.FromProfile(p, "mi300x")[0]
	ras := node(1) + "/ras/"
	pages := card(0) + "/ras/gpu_vram_bad_pages"

	g = apply(t, tree, g, func(g *fleet.GPU) { g.Status, g.RetiredPages = fleet.PageRetired, 3 })
	if strings.Count(read(pages), " : 0x00001000 : R\n") != 3 || read(ras+"fatal_error") != "0\n" {
		t.Fatal("retired pages are listed and are not fatal")
	}
	g = apply(t, tree, g, func(g *fleet.GPU) { g.Status, g.RetiredPages = fleet.PageRetired, 5000 })
	if strings.Count(read(pages), "\n") != MaxBadPages {
		t.Fatal("the bad page list is bounded")
	}
	g = apply(t, tree, g, func(g *fleet.GPU) { g.Status, g.RetiredPages, g.ECCErrors = fleet.ECCError, 0, 2 })
	if read(ras+"fatal_error") != "1\n" || read(ras+"ecc_uncorrectable") != "2\n" || read(pages) != "" {
		t.Fatal("ECC")
	}
	g = apply(t, tree, g, func(g *fleet.GPU) { g.Status, g.ECCErrors = fleet.Crashed, 0 })
	binding := bound + "/" + g.BDF
	if read(ras+"fatal_error") != "1\n" || exists(t, tree, binding) {
		t.Fatal("a crash unbinds the device")
	}
	apply(t, tree, g, func(g *fleet.GPU) { g.Status = fleet.Healthy })
	if read(ras+"fatal_error") != "0\n" || !strings.Contains(read(binding+"/uevent"), "PCI_SLOT_NAME="+g.BDF) || read(binding+"/current_compute_partition") != "SPX\n" {
		t.Fatal("recovery rebinds the device as rendered")
	}
}

func TestXGMILinksNeedBothEnds(t *testing.T) {
	p := catalog(t).Profiles["mi300x"]
	tree, read := rendered(t, p)
	gpus := fleet.FromProfile(p, "mi300x")
	up := func(from, to int) bool {
		t.Helper()
		for i := 1; i < len(p.Devices); i++ {
			k := props(read(fmt.Sprintf("%s/io_links/%d/properties", node(from+1), i)))
			if k["node_to"] == strconv.Itoa(to+1) {
				return k["flags"] == "0x0000000000000001"
			}
		}
		t.Fatalf("no link %d-%d", from, to)
		return false
	}
	set := func(i int, s fleet.Status) { apply(t, tree, gpus[i], func(g *fleet.GPU) { g.Status = s }) }

	set(0, fleet.XGMIDown)
	if up(0, 1) || up(1, 0) || !up(1, 2) || read(card(0)+"/xgmi_error") != "1\n" {
		t.Fatal("GPU 0 down")
	}
	if !strings.Contains(read(node(1)+"/io_links/0/properties"), "flags 0x0000000000000001") {
		t.Fatal("PCIe link to the CPU went down")
	}
	set(1, fleet.XGMIDown)
	set(0, fleet.Healthy)
	if up(0, 1) || !up(0, 2) {
		t.Fatal("a link stays down while one end is down")
	}
	set(1, fleet.Healthy)
	if !up(0, 1) || read(card(0)+"/xgmi_error") != "0\n" {
		t.Fatal("links restored")
	}
}

// Without a host device directory a crash stays inside the root.
func TestCrashManagesOnlyTheConfiguredDeviceDir(t *testing.T) {
	base := catalog(t).Profiles["mi300x"]
	p := *base
	p.Devices = append([]profile.Device(nil), base.Devices...)
	for i := range p.Devices {
		p.Devices[i].RenderMinor += 5 // an iGPU took renderD128
	}
	dev := t.TempDir()
	plant := func() {
		for _, n := range []string{"dri/renderD128", "dri/renderD133", "dri/card0"} {
			writeFile(filepath.Join(dev, n), "")
		}
	}
	there := func(n string) bool { _, err := os.Stat(filepath.Join(dev, n)); return err == nil }
	g := fleet.FromProfile(&p, "mi300x")[0]

	plant()
	unmanaged, _ := rendered(t, &p)
	apply(t, unmanaged, g, func(g *fleet.GPU) { g.Status = fleet.Crashed })
	if !there("dri/renderD133") || !there("dri/card0") {
		t.Fatal("a tree without a device directory removed nodes")
	}

	managed, _ := rendered(t, &p)
	managed.SetHostDev(dev)
	apply(t, managed, g, func(g *fleet.GPU) { g.Status = fleet.Crashed })
	if there("dri/renderD133") || there("dri/card0") || !there("dri/renderD128") {
		t.Fatal("a crash removes the card's own nodes and nothing else")
	}
	apply(t, managed, g, func(g *fleet.GPU) { g.Status = fleet.Healthy })
	binding := bound + "/" + g.BDF + "/drm"
	if !exists(t, managed, binding+"/renderD133") || exists(t, managed, binding+"/renderD128") {
		t.Fatal("recovery binds the profile's render minor")
	}
}

// Entries of the console's virtual partition modes are display state.
func TestVirtualEntriesTouchNothing(t *testing.T) {
	p := catalog(t).Profiles["mi300x"]
	tree, _ := rendered(t, p)
	log := filepath.Join(t.TempDir(), "kmsg")
	tree.SetKernelLog(log)
	physical := fleet.FromProfile(p, "mi300x")[1]
	for _, g := range []fleet.GPU{
		{Index: 40, BDF: physical.BDF, Status: fleet.Crashed},
		{Index: 1, BDF: p.Devices[0].BDF, Status: fleet.Crashed}, // slice 1 of card 0 in CPX
	} {
		for _, write := range []func(*fleet.GPU) error{tree.Apply, tree.Telemetry, tree.SMI} {
			if err := write(&g); err != nil {
				t.Fatal(err)
			}
		}
	}
	if exists(t, tree, card(40)) || exists(t, tree, "smi/gpu40") || !exists(t, tree, bound+"/"+physical.BDF) {
		t.Fatal("a virtual entry changed the rendered tree")
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("a virtual entry reached the kernel log")
	}
}

func TestKernelLog(t *testing.T) {
	p := catalog(t).Profiles["mi300x"]
	tree, _ := rendered(t, p)
	path := filepath.Join(t.TempDir(), "kernel.log")
	tree.SetKernelLog(path)
	gpus := fleet.FromProfile(p, "mi300x")
	for _, step := range []struct {
		i int
		s fleet.Status
	}{{0, fleet.Healthy}, {0, fleet.PageRetired}, {0, fleet.Healthy}, {1, fleet.XGMIDown}, {2, fleet.Throttling},
		{3, fleet.Overheating}, {4, fleet.ECCError}, {5, fleet.Crashed}, {5, fleet.Healthy}, {6, fleet.Status("odd")}} {
		apply(t, tree, gpus[step.i], func(g *fleet.GPU) { g.Status, g.RetiredPages, g.ECCErrors = step.s, 2, 3 })
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// "amdgpu:" lines are the real driver's formats; {n} is its event number.
	want := []string{
		`^amdgpu 0000:05:00\.0: fake-gpu: 2 VRAM page\(s\) retired$`,
		`^amdgpu 0000:05:00\.0: fake-gpu: recovered$`,
		`^amdgpu 0000:26:00\.0: amdgpu: \{4\} socket: 0, die: 0, 1 new uncorrectable hardware errors detected in xgmi_wafl block$`,
		`^amdgpu 0000:46:00\.0: amdgpu: WARN: GPU thermal throttling temperature reached, expect performance decrease\. TEMP_HOTSPOT\.$`,
		`^amdgpu 0000:65:00\.0: amdgpu: ERROR: GPU over temperature range\(SW CTF\) detected!$`,
		`^amdgpu 0000:85:00\.0: amdgpu: \{7\} socket: 0, die: 0, 3 new uncorrectable hardware errors detected in umc block$`,
		`^amdgpu 0000:a6:00\.0: fake-gpu: device lost$`,
		`^amdgpu 0000:a6:00\.0: amdgpu: GPU reset\(1\) succeeded!$`,
		`^amdgpu 0000:c6:00\.0: fake-gpu: status odd$`,
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != len(want) {
		t.Fatalf("%d lines, want %d:\n%s", len(lines), len(want), data)
	}
	for i, pattern := range want {
		if !regexp.MustCompile(pattern).MatchString(lines[i]) {
			t.Errorf("line %d %q does not match %s", i, lines[i], pattern)
		}
	}
}

func TestTelemetry(t *testing.T) {
	p := catalog(t).Profiles["mi300x"]
	tree, read := rendered(t, p)
	attr := func(name string) string { return strings.TrimSpace(read(card(0) + "/" + name)) }
	for name, want := range map[string]string{
		"mem_info_vram_total": "206158430208", "hwmon/hwmon0/power1_cap_max": "750000000", "current_link_speed": "32.0 GT/s PCIe",
		"pp_dpm_sclk": "0: 500Mhz *\n1: 2100Mhz", "hwmon/hwmon0/temp2_label": "junction", "hwmon/hwmon0/freq1_label": "sclk",
		"hwmon/hwmon0/power1_label": "PPT", "hwmon/hwmon0/freq2_input": "1300000000", "power_dpm_force_performance_level": "auto",
	} {
		if got := attr(name); got != want {
			t.Errorf("boot %s = %q, want %q", name, got, want)
		}
	}
	g := fleet.FromProfile(p, "mi300x")[0]
	g.Util, g.MemUsedMB, g.TempC, g.PowerW, g.GFXClockMHz = 91, 4096, 70, 600, 2050
	if err := tree.Telemetry(&g); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"gpu_busy_percent": "91", "mem_info_vram_used": "4294967296", "mem_info_vis_vram_used": "4294967296",
		"hwmon/hwmon0/temp1_input": "70000", "hwmon/hwmon0/temp2_input": "76000", "hwmon/hwmon0/temp3_input": "66000",
		"hwmon/hwmon0/power1_average": "600000000", "hwmon/hwmon0/freq1_input": "2050000000", "pp_dpm_sclk": "0: 500Mhz\n1: 2100Mhz *",
	} {
		if got := attr(name); got != want {
			t.Errorf("live %s = %q, want %q", name, got, want)
		}
	}
	if tmp, _ := filepath.Glob(filepath.Join(tree.Root(), card(0), "*.tmp")); len(tmp) != 0 {
		t.Fatalf("temporary files left: %v", tmp)
	}
}

func TestFormatting(t *testing.T) {
	for gen, want := range map[uint32]string{0: "Unknown", 1: "2.5 GT/s PCIe", 5: "32.0 GT/s PCIe", 6: "64.0 GT/s PCIe", 7: "Unknown"} {
		if linkSpeed(gen) != want {
			t.Errorf("gen %d: %s", gen, linkSpeed(gen))
		}
	}
	for _, c := range []struct {
		lo, hi, cur uint32
		want        string
	}{{0, 2100, 0, "0: 2100Mhz *"}, {2100, 2100, 0, "0: 2100Mhz *"}, {500, 2100, 500, "0: 500Mhz *\n1: 2100Mhz"}, {500, 2100, 1300, "0: 500Mhz\n1: 2100Mhz *"}} {
		if got := dpm(c.lo, c.hi, c.cur); got != c.want {
			t.Errorf("dpm(%d, %d, %d) = %q", c.lo, c.hi, c.cur, got)
		}
	}
}
