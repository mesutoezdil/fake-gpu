package kfd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The renderer writes what amdgpu exposes, so capturing a rendered profile
// must give the topology and compute description back.
func TestCaptureRoundTripsRenderedProfile(t *testing.T) {
	for slug, want := range profilesForTest(t) {
		t.Run(slug, func(t *testing.T) {
			root := t.TempDir()
			if err := NewRenderer(want, root).RenderAll(); err != nil {
				t.Fatal(err)
			}
			got, notes, err := CaptureProfile(root, CaptureOptions{KeepIDs: true})
			if err != nil {
				t.Fatal(err)
			}
			w, g := want.DeviceDefault, got.DeviceDefault
			if g.DeviceID != w.DeviceID || g.GFXTargetVersion != w.GFXTargetVersion || g.ProductName != w.SysfsProductName() || g.Memory.VRAMSizeBytes != w.Memory.VRAMSizeBytes {
				t.Fatalf("identity: got %+v", g)
			}
			if g.Compute.SIMDCount != w.Compute.SIMDCount || g.Compute.WaveFrontSize != w.Compute.WaveFrontSize || g.Compute.MaxWavesPerSIMD != w.Compute.MaxWavesPerSIMD || g.Compute.NumSDMAXGMIEngines != w.Compute.NumSDMAXGMIEngines {
				t.Fatalf("compute: got %+v want %+v", g.Compute, w.Compute)
			}
			if len(got.Devices) != len(want.Devices) {
				t.Fatalf("captured %d devices, want %d", len(got.Devices), len(want.Devices))
			}
			for i, dev := range got.Devices {
				if dev.PCIBDF != want.Devices[i].PCIBDF || dev.DRMRenderMinor != want.Devices[i].DRMRenderMinor || dev.XGMIDeviceID != want.Devices[i].XGMIDeviceID {
					t.Fatalf("device %d: got %+v want %+v", i, dev, want.Devices[i])
				}
			}
			if !reflect.DeepEqual(got.PCIETopology, want.PCIETopology) {
				t.Fatalf("topology: got %+v want %+v", got.PCIETopology, want.PCIETopology)
			}
			wantHive, err := parseID(want.XGMI.HiveID)
			gotHive, _ := parseID(got.XGMI.HiveID)
			if err != nil || wantHive == 0 || gotHive != wantHive || got.XGMI.LinksPerGPU != uint32(len(want.Devices)-1) {
				t.Fatalf("xGMI: got %+v want hive %q (%v)", got.XGMI, want.XGMI.HiveID, err)
			}
			// Attributes read from the PCI device and its hwmon directory.
			if g.SubsystemDeviceID != w.SubsystemDeviceID || g.Memory.VRAMUsedBytes != w.Memory.VRAMUsedBytes || g.Memory.GTTSizeBytes != w.Memory.GTTSizeBytes {
				t.Fatalf("device attributes: got %+v", g)
			}
			if g.Power.MaxPowerCapW != w.Power.MaxPowerCapW || g.Power.DefaultPowerCapW != w.Power.DefaultPowerCapW || g.Power.MinPowerCapW != w.Power.MinPowerCapW || g.Power.CurrentSocketPowerW != w.Power.CurrentSocketPowerW {
				t.Fatalf("power: got %+v want %+v", g.Power, w.Power)
			}
			if g.Thermal != w.Thermal || g.PCIe.Gen != w.PCIe.Gen || g.PCIe.Width != w.PCIe.Width || g.PCIe.MaxGen != w.PCIe.MaxGen || g.PCIe.MaxWidth != w.PCIe.MaxWidth {
				t.Fatalf("thermal %+v pcie %+v, want %+v %+v", g.Thermal, g.PCIe, w.Thermal, w.PCIe)
			}
			if g.Clocks.MinGFXClkMHz != w.Clocks.MinGFXClkMHz || g.Clocks.MaxGFXClkMHz != w.Clocks.MaxGFXClkMHz || g.Clocks.CurrentGFXClkMHz != w.Clocks.CurrentGFXClkMHz || g.Clocks.MaxMemClkMHz != w.Clocks.MaxMemClkMHz {
				t.Fatalf("clocks: got %+v want %+v", g.Clocks, w.Clocks)
			}
			if len(notes) != 0 {
				t.Fatalf("rendered tree left values unreadable: %v", notes)
			}
			// The captured profile has to be usable as a mock profile again.
			if err := NewRenderer(got, t.TempDir()).RenderAll(); err != nil {
				t.Fatalf("captured profile does not render: %v", err)
			}
		})
	}
}

// Layout and values as read from a desktop with a discrete card and an iGPU.
// The discrete card is runtime-suspended, so its live sensors are absent.
func TestCaptureRealLayout(t *testing.T) {
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
	if err := os.MkdirAll(filepath.Join(root, "sys/bus/pci/devices"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, card), filepath.Join(root, "sys/bus/pci/devices/0000:03:00.0")); err != nil {
		t.Fatal(err)
	}
	write("proc/sys/kernel/osrelease", "7.0.0-43-generic\n")

	p, notes, err := CaptureProfile(root, CaptureOptions{Name: "Radeon RX 9070 XT"})
	if err != nil {
		t.Fatal(err)
	}
	d := p.DeviceDefault
	if len(p.Devices) != 1 || p.Devices[0].PCIBDF != "0000:03:00.0" || p.Devices[0].DRMRenderMinor != 128 {
		t.Fatalf("devices %+v", p.Devices)
	}
	if strings.Contains(p.Devices[0].UniqueID, "f03cdc17723f279a") {
		t.Fatal("real unique ID leaked without --keep-ids")
	}
	if d.Name != "Radeon RX 9070 XT" || d.DeviceID != 0x7550 || d.SubsystemVendorID != 0x1849 || d.Compute.CUCount != 64 || d.Memory.VRAMSizeBytes != 17095983104 || d.Memory.VRAMType != "samsung" {
		t.Fatalf("defaults %+v", d)
	}
	if d.PCIe.Gen != 5 || d.PCIe.Width != 16 || d.Thermal.SlowdownTempC != 110 || d.Thermal.ShutdownTempC != 115 || p.System.AMDGPUVersion != "7.0.0-43-generic" {
		t.Fatalf("pcie %+v thermal %+v system %+v", d.PCIe, d.Thermal, p.System)
	}
	if rc := p.PCIETopology.RootComplexes; len(rc) != 1 || rc[0].ID != "pci0000:00" || rc[0].NUMANode != 0 {
		t.Fatalf("topology %+v", rc)
	}
	joined := strings.Join(notes, "\n")
	for _, want := range []string{"left out 0000:0f:00.0", "max_power_cap_w is 0", "pp_dpm_sclk not readable"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("notes lack %q:\n%s", want, joined)
		}
	}
	if got, _, err := CaptureProfile(root, CaptureOptions{BDF: "0000:0f:00.0"}); err != nil || got.DeviceDefault.DeviceID != 0x13c0 {
		t.Fatalf("--gpu selection: %v %+v", err, got)
	}
	if _, _, err := CaptureProfile(root, CaptureOptions{BDF: "0000:99:00.0"}); err == nil {
		t.Fatal("unknown GPU accepted")
	}
	if _, _, err := CaptureProfile(t.TempDir(), CaptureOptions{}); err == nil {
		t.Fatal("empty sysfs accepted")
	}
}
