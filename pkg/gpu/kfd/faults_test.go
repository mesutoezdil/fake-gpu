package kfd

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func postAction(t *testing.T, api *APIServer, path string, wantCode int) GPUState {
	t.Helper()
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("POST", path, nil))
	if rec.Code != wantCode {
		t.Fatalf("%s: code %d: %s", path, rec.Code, rec.Body.String())
	}
	var g GPUState
	if wantCode == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
			t.Fatal(err)
		}
	}
	return g
}

func faultFixture(t *testing.T) (*APIServer, *FleetState, func(string) string) {
	t.Helper()
	p := profilesForTest(t)["mi300x"]
	root := t.TempDir()
	r := NewRenderer(p, root)
	if err := r.RenderAll(); err != nil {
		t.Fatal(err)
	}
	state := NewFleetState(p, "mi300x")
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	return NewAPIServer(state, r), state, read
}

// tests/dashboard-e2e.py looks for the same title on a live cluster; keeping
// the check here catches a renamed page before it reaches CI.
func TestConsolePageIsServed(t *testing.T) {
	api := NewAPIServer(NewFleetState(profilesForTest(t)["mi300x"], "mi300x"), nil)
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	page := rec.Body.String()
	for _, want := range []string{"<title>fake-gpu console</title>", "/api/gpus", "/api/actions/", "</html>"} {
		if rec.Code != 200 || !strings.Contains(page, want) {
			t.Fatalf("console page (HTTP %d) lacks %q", rec.Code, want)
		}
	}
	data, err := os.ReadFile("../../../tests/dashboard-e2e.py")
	if err != nil || !strings.Contains(string(data), "<title>fake-gpu console</title>") {
		t.Fatalf("tests/dashboard-e2e.py no longer checks the served title: %v", err)
	}
}

func TestRetirePageReachesSysfs(t *testing.T) {
	api, _, read := faultFixture(t)
	const badPages = "sys/class/drm/card0/device/ras/gpu_vram_bad_pages"
	if read(badPages) != "" {
		t.Fatal("healthy GPU lists bad pages")
	}
	g := postAction(t, api, "/api/actions/retire-page?gpu=0&count=3", 200)
	if g.Status != "page_retired" || g.RetiredPages != 3 {
		t.Fatalf("state %+v", g)
	}
	if got := read(badPages); strings.Count(got, " : 0x00001000 : R\n") != 3 {
		t.Fatalf("bad pages file %q", got)
	}
	if read("sys/class/kfd/kfd/topology/nodes/1/ras/fatal_error") != "0\n" {
		t.Fatal("a retired page must not raise fatal_error")
	}
	if g = postAction(t, api, "/api/actions/recover?gpu=0", 200); g.RetiredPages != 0 || read(badPages) != "" {
		t.Fatalf("recover left retired pages: %+v", g)
	}
}

func TestXGMILinkDownDisablesBothEnds(t *testing.T) {
	api, _, read := faultFixture(t)
	// enabled reports the xGMI link flag from one KFD node to another.
	enabled := func(from, to string) bool {
		t.Helper()
		for i := 1; i < 8; i++ {
			props := read("sys/class/kfd/kfd/topology/nodes/" + from + "/io_links/" + string(rune('0'+i)) + "/properties")
			if strings.Contains(props, "node_to "+to+"\n") {
				return strings.Contains(props, "flags 0x0000000000000001")
			}
		}
		t.Fatalf("no link %s->%s", from, to)
		return false
	}
	postAction(t, api, "/api/actions/xgmi-down?gpu=0", 200)
	if enabled("1", "2") || enabled("2", "1") {
		t.Fatal("links of the faulted GPU stay enabled")
	}
	if !enabled("2", "3") {
		t.Fatal("unrelated link went down")
	}
	if !strings.Contains(read("sys/class/kfd/kfd/topology/nodes/1/io_links/0/properties"), "flags 0x0000000000000001") {
		t.Fatal("PCIe link to the CPU went down")
	}
	if read("sys/class/drm/card0/device/xgmi_error") != "1\n" {
		t.Fatal("xgmi_error not raised")
	}
	// A link stays down until both of its ends recover.
	postAction(t, api, "/api/actions/xgmi-down?gpu=1", 200)
	postAction(t, api, "/api/actions/recover?gpu=0", 200)
	if enabled("1", "2") || !enabled("1", "3") {
		t.Fatal("recovering one end must restore only links to healthy peers")
	}
	postAction(t, api, "/api/actions/recover?gpu=1", 200)
	if !enabled("1", "2") || read("sys/class/drm/card0/device/xgmi_error") != "0\n" {
		t.Fatal("links not restored")
	}
}

func TestFaultsRejectCrashedGPU(t *testing.T) {
	api, _, _ := faultFixture(t)
	postAction(t, api, "/api/actions/crash?gpu=2", 200)
	for _, action := range []string{"throttle", "retire-page", "xgmi-down"} {
		postAction(t, api, "/api/actions/"+action+"?gpu=2", 409)
	}
	postAction(t, api, "/api/actions/throttle?gpu=99", 404)
}

// A crash takes the card's device nodes away, but only in the directory the
// renderer was told to manage. Left unset, nothing outside its root is touched.
func TestCrashManagesOnlyTheConfiguredDeviceDir(t *testing.T) {
	p := profilesForTest(t)["mi300x"]
	dev := t.TempDir()
	nodes := []string{filepath.Join(dev, "dri/renderD128"), filepath.Join(dev, "dri/card0")}
	plant := func() {
		t.Helper()
		for _, node := range nodes {
			if err := writeFile(node, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	present := func() int {
		n := 0
		for _, node := range nodes {
			if _, err := os.Stat(node); err == nil {
				n++
			}
		}
		return n
	}

	unmanaged := NewRenderer(p, t.TempDir())
	if err := unmanaged.RenderAll(); err != nil {
		t.Fatal(err)
	}
	plant()
	postAction(t, NewAPIServer(NewFleetState(p, "mi300x"), unmanaged), "/api/actions/crash?gpu=0", 200)
	if present() != 2 {
		t.Fatal("a renderer without a device directory removed device nodes")
	}

	managed := NewRenderer(p, t.TempDir())
	if err := managed.RenderAll(); err != nil {
		t.Fatal(err)
	}
	managed.SetHostDevDir(dev)
	postAction(t, NewAPIServer(NewFleetState(p, "mi300x"), managed), "/api/actions/crash?gpu=0", 200)
	if present() != 0 {
		t.Fatal("crash left the card and render nodes in the managed directory")
	}
}

func TestTelemetryFollowsLoad(t *testing.T) {
	api, state, _ := faultFixture(t)
	sim := NewDynamicSimulator(state)
	settle := func() (busy, idle GPUState) {
		for i := 0; i < 200; i++ {
			sim.tick()
		}
		busy, _ = state.GetGPU(0)
		idle, _ = state.GetGPU(1)
		return
	}
	postAction(t, api, "/api/actions/busy?gpu=0&util=90", 200)
	busy, idle := settle()
	if busy.PowerW < 2*idle.PowerW || busy.TemperatureC < idle.TemperatureC+10 {
		t.Fatalf("load does not drive telemetry: busy %dW %dC, idle %dW %dC", busy.PowerW, busy.TemperatureC, idle.PowerW, idle.TemperatureC)
	}
	if busy.Throttled || busy.ClockGFXMHz < 1800 {
		t.Fatalf("busy GPU throttled without cause: %+v", busy)
	}

	state.UpdateGPU(0, json.RawMessage(`{"status":"throttling"}`))
	throttled, _ := settle()
	if !throttled.Throttled || throttled.PowerW > busy.PowerCapW/2+5 || throttled.ClockGFXMHz >= 1800 {
		t.Fatalf("throttle has no effect: before %+v after %+v", busy, throttled)
	}

	// Past the junction limit the GPU throttles itself.
	state.UpdateGPU(1, json.RawMessage(`{"status":"overheating","temperature_c":105,"utilization_pct":90}`))
	_, hot := settle()
	if !hot.Throttled || hot.TemperatureC != 105 {
		t.Fatalf("overheating GPU not throttled: %+v", hot)
	}
}

func TestLiveTelemetryReachesSysfs(t *testing.T) {
	p := profilesForTest(t)["mi300x"]
	root := t.TempDir()
	r := NewRenderer(p, root)
	if err := r.RenderAll(); err != nil {
		t.Fatal(err)
	}
	state := NewFleetState(p, "mi300x")
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, "sys/class/drm/card0/device", name))
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(b))
	}
	if read("mem_info_vram_total") != "206158430208" || read("hwmon/hwmon0/power1_cap_max") != "750000000" || read("current_link_speed") != "32.0 GT/s PCIe" || read("pp_dpm_sclk") != "0: 500Mhz *\n1: 2100Mhz" {
		t.Fatalf("static attributes: vram %s cap %s link %s sclk %q", read("mem_info_vram_total"), read("hwmon/hwmon0/power1_cap_max"), read("current_link_speed"), read("pp_dpm_sclk"))
	}
	if read("hwmon/hwmon0/temp2_label") != "junction" || read("hwmon/hwmon0/freq1_label") != "sclk" || read("hwmon/hwmon0/power1_label") != "PPT" || read("hwmon/hwmon0/freq2_input") != "1300000000" || read("power_dpm_force_performance_level") != "auto" {
		t.Fatalf("hwmon labels: %q %q %q, mclk %s", read("hwmon/hwmon0/temp2_label"), read("hwmon/hwmon0/freq1_label"), read("hwmon/hwmon0/power1_label"), read("hwmon/hwmon0/freq2_input"))
	}
	// The simulator, not only API actions, has to keep the sensors current.
	sim := NewDynamicSimulator(state, func(g *GPUState) {
		if err := r.WriteTelemetrySysfs(g); err != nil {
			t.Fatal(err)
		}
	})
	state.UpdateGPU(0, json.RawMessage(`{"target_utilization_pct":90,"memory_used_mb":4096}`))
	for i := 0; i < 50; i++ {
		sim.tick()
	}
	g, _ := state.GetGPU(0)
	if read("gpu_busy_percent") != strconv.Itoa(g.UtilizationPct) || g.UtilizationPct < 87 || read("mem_info_vram_used") != "4294967296" || read("mem_info_vis_vram_used") != "4294967296" ||
		read("hwmon/hwmon0/temp1_input") != strconv.Itoa(g.TemperatureC*1000) || read("hwmon/hwmon0/power1_average") != strconv.Itoa(g.PowerW*1000000) ||
		read("pp_dpm_sclk") != "0: 500Mhz\n1: 2100Mhz *" {
		t.Fatalf("live attributes stale: busy %s temp %s power %s sclk %q state %+v", read("gpu_busy_percent"), read("hwmon/hwmon0/temp1_input"), read("hwmon/hwmon0/power1_average"), read("pp_dpm_sclk"), g)
	}
	if tmp, _ := filepath.Glob(filepath.Join(root, "sys/class/drm/card0/device/*.tmp")); len(tmp) != 0 {
		t.Fatalf("temporary files left behind: %v", tmp)
	}
	// Display-only virtual partitions must not grow new card directories.
	if err := r.WriteTelemetrySysfs(&GPUState{Index: 40}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "sys/class/drm/card40")); err == nil {
		t.Fatal("telemetry created a card that was never rendered")
	}
}

func TestHIPClientMemoryCountsAsVRAM(t *testing.T) {
	p := profilesForTest(t)["mi300x"]
	root := t.TempDir()
	state := NewFleetState(p, "mi300x")
	sim := NewDynamicSimulator(state)
	sim.TrackHIP(root)
	sim.tick()
	before, _ := state.GetGPU(0)

	if err := os.MkdirAll(filepath.Join(root, "hip"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A live client: its file is locked. A dead one: the file is left unlocked.
	live, err := os.OpenFile(filepath.Join(root, "hip/host-100"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(live.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	live.WriteString("0 68719476736\n2 1073741824\n")
	dead := filepath.Join(root, "hip/host-200")
	if err := os.WriteFile(dead, []byte("0 34359738368\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sim.tick()
	g0, _ := state.GetGPU(0)
	g2, _ := state.GetGPU(2)
	if g0.MemoryUsedMB != before.MemoryUsedMB+65536 || g2.MemoryUsedMB != before.MemoryUsedMB+1024 {
		t.Fatalf("VRAM in use: gpu0 %d MB, gpu2 %d MB, baseline %d MB", g0.MemoryUsedMB, g2.MemoryUsedMB, before.MemoryUsedMB)
	}
	if _, err := os.Stat(dead); err == nil {
		t.Fatal("usage file of a dead client was kept")
	}

	live.Close() // the client exits: its lock goes away with it
	sim.tick()
	if g0, _ = state.GetGPU(0); g0.MemoryUsedMB != before.MemoryUsedMB {
		t.Fatalf("VRAM not released: %d MB, baseline %d MB", g0.MemoryUsedMB, before.MemoryUsedMB)
	}
}

func TestFaultTransitionsReachKernelLog(t *testing.T) {
	p := profilesForTest(t)["mi300x"]
	root := t.TempDir()
	r := NewRenderer(p, root)
	if err := r.RenderAll(); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "kernel.log")
	r.SetKernelLog(logPath)
	api := NewAPIServer(NewFleetState(p, "mi300x"), r)
	postAction(t, api, "/api/actions/busy?gpu=0", 200) // healthy to healthy: no line
	postAction(t, api, "/api/actions/retire-page?gpu=0&count=2", 200)
	postAction(t, api, "/api/actions/recover?gpu=0", 200)
	postAction(t, api, "/api/actions/xgmi-down?gpu=1", 200)
	postAction(t, api, "/api/actions/throttle?gpu=2", 200)
	postAction(t, api, "/api/actions/overheat?gpu=3", 200)
	postAction(t, api, "/api/actions/ecc-error?gpu=4&count=3", 200)
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	// Formats with an "amdgpu:" tag are the real driver's; "{n}" is its event number.
	want := []string{
		`^amdgpu 0000:05:00\.0: fake-gpu: 2 VRAM page\(s\) retired$`,
		`^amdgpu 0000:05:00\.0: fake-gpu: recovered$`,
		`^amdgpu 0000:26:00\.0: amdgpu: \{\d+\} socket: 0, die: 0, 1 new uncorrectable hardware errors detected in xgmi_wafl block$`,
		`^amdgpu 0000:46:00\.0: amdgpu: WARN: GPU thermal throttling temperature reached, expect performance decrease\. TEMP_HOTSPOT\.$`,
		`^amdgpu 0000:65:00\.0: amdgpu: ERROR: GPU over temperature range\(SW CTF\) detected!$`,
		`^amdgpu 0000:85:00\.0: amdgpu: \{\d+\} socket: 0, die: 0, 3 new uncorrectable hardware errors detected in umc block$`,
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != len(want) {
		t.Fatalf("kernel log has %d lines, want %d:\n%s", len(lines), len(want), data)
	}
	for i, pattern := range want {
		if !regexp.MustCompile(pattern).MatchString(lines[i]) {
			t.Fatalf("line %d: %q does not match %s", i, lines[i], pattern)
		}
	}
}

func TestBusyHoldsRequestedLoad(t *testing.T) {
	api, state, _ := faultFixture(t)
	sim := NewDynamicSimulator(state)
	postAction(t, api, "/api/actions/busy?gpu=0&util=95", 200)
	for i := 0; i < 2000; i++ {
		sim.tick()
		if g, _ := state.GetGPU(0); g.UtilizationPct < 92 || g.UtilizationPct > 98 {
			t.Fatalf("tick %d: utilization %d drifted from the 95%% target", i, g.UtilizationPct)
		}
	}
	postAction(t, api, "/api/actions/idle?gpu=0", 200)
	sim.tick()
	if g, _ := state.GetGPU(0); g.TargetUtilPct != 0 || g.UtilizationPct >= 8 {
		t.Fatalf("idle left load behind: %+v", g)
	}
}

func TestSMIStateRoundTrip(t *testing.T) {
	p := profilesForTest(t)["mi300x"]
	root := t.TempDir()
	r := NewRenderer(p, root)
	if err := r.renderSMIState(); err != nil {
		t.Fatal(err)
	}
	want, _ := NewFleetState(p, "mi300x").GetGPU(3)
	want.Status, want.Throttled, want.RetiredPages, want.PowerW = "page_retired", true, 4, 321
	if err := r.WriteSMIState(&want); err != nil {
		t.Fatal(err)
	}
	gpus, err := ReadSMIState(filepath.Join(root, "smi"))
	if err != nil || len(gpus) != len(p.Devices) {
		t.Fatalf("read: %v (%d GPUs)", err, len(gpus))
	}
	got := gpus[3]
	if got.Status != want.Status || !got.Throttled || got.RetiredPages != 4 || got.PowerW != 321 || got.Name != want.Name || got.PCIBDF != want.PCIBDF || got.UUID != smiUUID(want.UUID) {
		t.Fatalf("round trip lost state: %+v", got)
	}
}
