package capture

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mesutoezdil/fake-gpu/pkg/fleet"
	"github.com/mesutoezdil/fake-gpu/pkg/profile"
	"github.com/mesutoezdil/fake-gpu/pkg/sysfs"
)

// The renderer writes what amdgpu exposes, so capturing a rendered tree has
// to give back the profile's hardware description.
func TestCaptureRoundTrip(t *testing.T) {
	c, err := profile.LoadDir("../../profiles")
	if err != nil {
		t.Fatal(err)
	}
	for _, slug := range c.Slugs {
		want := c.Profiles[slug]
		t.Run(slug, func(t *testing.T) {
			root := t.TempDir()
			if err := sysfs.New(want, root).Render(); err != nil {
				t.Fatal(err)
			}
			got, notes, err := Capture(root, Options{KeepIDs: true})
			if err != nil {
				t.Fatal(err)
			}
			if len(notes) != 0 {
				t.Fatalf("a rendered tree left values unreadable: %v", notes)
			}
			w, g := want.Card, got.Card
			for name, pair := range map[string][2]any{
				"device":     {w.Device, g.Device},
				"subsystem":  {w.SubsystemDevice, g.SubsystemDevice},
				"gfx target": {w.GFXTarget, g.GFXTarget},
				"product":    {w.ProductName(), g.Product},
				"vram":       {w.Memory.VRAM, g.Memory.VRAM},
				"vram used":  {w.Memory.VRAMUsed, g.Memory.VRAMUsed},
				"gtt":        {w.Memory.GTT, g.Memory.GTT},
				"compute":    {[4]uint32{w.Compute.SIMDs, w.Compute.Wavefront, w.Compute.WavesPerSIMD, w.Compute.SDMAXGMIEngines}, [4]uint32{g.Compute.SIMDs, g.Compute.Wavefront, g.Compute.WavesPerSIMD, g.Compute.SDMAXGMIEngines}},
				"power":      {w.Power, g.Power},
				"thermal":    {w.Thermal, g.Thermal},
				"pcie":       {[4]uint32{w.PCIe.Gen, w.PCIe.Width, w.PCIe.MaxGen, w.PCIe.MaxWidth}, [4]uint32{g.PCIe.Gen, g.PCIe.Width, g.PCIe.MaxGen, g.PCIe.MaxWidth}},
				"gfx clocks": {[3]uint32{w.Clocks.GFXMin, w.Clocks.GFXMax, w.Clocks.GFX}, [3]uint32{g.Clocks.GFXMin, g.Clocks.GFXMax, g.Clocks.GFX}},
				"mem clock":  {w.Clocks.MemMax, g.Clocks.MemMax},
				"driver":     {want.Software.Driver, got.Software.Driver},
				"topology":   {want.Topology, got.Topology},
			} {
				if !reflect.DeepEqual(pair[0], pair[1]) {
					t.Errorf("%s: got %+v, want %+v", name, pair[1], pair[0])
				}
			}
			if len(got.Devices) != len(want.Devices) {
				t.Fatalf("%d devices, want %d", len(got.Devices), len(want.Devices))
			}
			for i, d := range got.Devices {
				w := want.Devices[i]
				if d.BDF != w.BDF || d.RenderMinor != w.RenderMinor || d.XGMIID != w.XGMIID {
					t.Fatalf("device %d: %+v, want %+v", i, d, w)
				}
				wantID, _ := profile.ParseID(w.UniqueID)
				gotID, _ := profile.ParseID(d.UniqueID)
				if wantID != gotID {
					t.Fatalf("--keep-ids lost the unique ID of device %d", i)
				}
			}
			hive, _ := profile.ParseID(want.XGMI.Hive)
			gotHive, _ := profile.ParseID(got.XGMI.Hive)
			if hive != gotHive || (hive != 0 && got.XGMI.LinksPerGPU != uint32(len(want.Devices)-1)) {
				t.Fatalf("xGMI %+v, want hive %s", got.XGMI, want.XGMI.Hive)
			}
			// And the capture has to drive the mock again.
			if err := sysfs.New(got, t.TempDir()).Render(); err != nil {
				t.Fatalf("captured profile does not render: %v", err)
			}
		})
	}
}

// The checked-in capture of a real card has to keep working.
func TestCapturedExamplesDriveTheMock(t *testing.T) {
	files, _ := filepath.Glob("../../examples/captured/*.yaml")
	if len(files) == 0 {
		t.Fatal("no captured examples")
	}
	for _, file := range files {
		p, err := profile.Load(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := sysfs.New(p, t.TempDir()).Render(); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		f := fleet.New(p, "captured")
		if _, err := f.Busy(0, 90, 50); err != nil {
			t.Fatal(err)
		}
		s := fleet.NewSimulator(f)
		for range 100 {
			s.Step()
		}
		g, _ := f.Get(0)
		limit := int(p.Card.Power.MaxCap)
		if limit == 0 || g.PowerW < limit/2 || g.PowerW > limit || g.TempC < 60 {
			t.Fatalf("%s: load does not move telemetry: %d of %d W, %d C", file, g.PowerW, limit, g.TempC)
		}
	}
}

// Layout and values read from a desktop with a discrete card and an iGPU.
// The discrete card is runtime suspended, so its live sensors are missing.
func TestCaptureDesktop(t *testing.T) {
	root := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		path = filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	nodes := "sys/class/kfd/kfd/topology/nodes/"
	write(nodes+"0/properties", "cpu_cores_count 24\nsimd_count 0\nvendor_id 0\n")
	write(nodes+"1/properties", "simd_count 128\nsimd_per_cu 2\nwave_front_size 32\ngfx_target_version 120001\nvendor_id 4098\ndevice_id 30032\nlocation_id 768\ndomain 0\ndrm_render_minor 128\nhive_id 0\nunique_id 17310953060964444058\nnum_xcc 1\n")
	write(nodes+"1/mem_banks/0/properties", "heap_type 1\nsize_in_bytes 17095983104\nwidth 256\nmem_clk_max 1258\n")
	write(nodes+"1/io_links/0/properties", "type 2\nnode_from 1\nnode_to 0\n")
	write(nodes+"2/properties", "simd_count 4\nsimd_per_cu 2\nvendor_id 4098\ndevice_id 5056\nlocation_id 3840\ndomain 0\ndrm_render_minor 129\n")
	// KFD counts the iGPU's shared memory: more than the discrete card has.
	write(nodes+"2/mem_banks/0/properties", "size_in_bytes 31423545344\n")
	write("sys/bus/pci/devices/0000:0f:00.0/mem_info_vram_total", "4294967296\n")
	card := "sys/devices/pci0000:00/0000:00:01.1/0000:03:00.0/"
	for name, value := range map[string]string{
		"uevent": "DRIVER=amdgpu\nPCI_SLOT_NAME=0000:03:00.0\n", "subsystem_vendor": "0x1849\n", "subsystem_device": "0x5414\n",
		"numa_node": "-1\n", "mem_info_vram_total": "17095983104\n", "mem_info_vram_used": "59912192\n", "mem_info_vram_vendor": "samsung\n",
		"current_link_speed": "32.0 GT/s PCIe\n", "current_link_width": "16\n", "max_link_speed": "32.0 GT/s PCIe\n", "max_link_width": "16\n",
		"hwmon/hwmon3/temp1_crit": "110000\n", "hwmon/hwmon3/temp1_emergency": "115000\n",
	} {
		write(card+name, value)
	}
	if err := os.Symlink(filepath.Join(root, card), filepath.Join(root, "sys/bus/pci/devices/0000:03:00.0")); err != nil {
		t.Fatal(err)
	}
	write("proc/sys/kernel/osrelease", "7.0.0-43-generic\n")

	p, notes, err := Capture(root, Options{Name: "Radeon RX 9070 XT"})
	if err != nil {
		t.Fatal(err)
	}
	d := p.Card
	if len(p.Devices) != 1 || p.Devices[0].BDF != "0000:03:00.0" || p.Devices[0].RenderMinor != 128 {
		t.Fatalf("devices %+v", p.Devices)
	}
	if strings.Contains(p.Devices[0].UniqueID, "f03cdc17723f279a") {
		t.Fatal("the real unique ID leaked without --keep-ids")
	}
	if d.Name != "Radeon RX 9070 XT" || d.Device != 0x7550 || d.SubsystemVendor != 0x1849 || d.Compute.CUs != 64 || d.Memory.VRAM != 17095983104 || d.Memory.Type != "samsung" {
		t.Fatalf("card %+v", d)
	}
	if d.PCIe.Gen != 5 || d.PCIe.Width != 16 || d.Thermal.Slowdown != 110 || d.Thermal.Shutdown != 115 || p.Software.Driver != "7.0.0-43-generic" {
		t.Fatalf("pcie %+v thermal %+v software %+v", d.PCIe, d.Thermal, p.Software)
	}
	if rc := p.Topology.RootComplexes; len(rc) != 1 || rc[0].ID != "pci0000:00" || rc[0].NUMA != 0 {
		t.Fatalf("topology %+v", rc)
	}
	joined := strings.Join(notes, "\n")
	for _, want := range []string{"left out 0000:0f:00.0", "max_power_cap_w is 0", "pp_dpm_sclk not readable", "edge temperature unknown"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("notes lack %q:\n%s", want, joined)
		}
	}
	if got, _, err := Capture(root, Options{BDF: "0000:0f:00.0"}); err != nil || got.Card.Device != 0x13c0 || got.Card.Name != "AMD GPU 1002:13c0" {
		t.Fatalf("--gpu: %v %+v", err, got)
	}
	if _, _, err := Capture(root, Options{BDF: "0000:99:00.0"}); err == nil {
		t.Fatal("unknown GPU accepted")
	}
	if _, _, err := Capture(t.TempDir(), Options{}); err == nil {
		t.Fatal("empty sysfs accepted")
	}
}
