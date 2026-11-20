package console

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/mesutoezdil/fake-gpu/pkg/fleet"
	"github.com/mesutoezdil/fake-gpu/pkg/profile"
	"github.com/mesutoezdil/fake-gpu/pkg/sysfs"
)

type fixture struct {
	t     *testing.T
	fleet *fleet.Fleet
	srv   *Server
}

func setup(t *testing.T, mode string, rendered bool) *fixture {
	t.Helper()
	c, err := profile.LoadDir("../../profiles")
	if err != nil {
		t.Fatal(err)
	}
	p, err := profile.Partition(c.Profiles["mi300x"], mode)
	if err != nil {
		t.Fatal(err)
	}
	f := fleet.New(p, "mi300x")
	f.AddCatalog(c)
	var tree *sysfs.Tree
	if rendered {
		tree = sysfs.New(p, t.TempDir())
		if err := tree.Render(); err != nil {
			t.Fatal(err)
		}
	}
	return &fixture{t, f, New(f, tree)}
}

func (x *fixture) do(method, url string, body string, code int) string {
	x.t.Helper()
	rec := httptest.NewRecorder()
	x.srv.ServeHTTP(rec, httptest.NewRequest(method, url, strings.NewReader(body)))
	if rec.Code != code {
		x.t.Fatalf("%s %s: HTTP %d, want %d: %s", method, url, rec.Code, code, rec.Body.String())
	}
	return rec.Body.String()
}

func (x *fixture) gpu(method, url string) fleet.GPU {
	x.t.Helper()
	var g fleet.GPU
	if err := json.Unmarshal([]byte(x.do(method, url, "", 200)), &g); err != nil {
		x.t.Fatal(err)
	}
	return g
}

// test/e2e/demo/dashboard.py checks the same title on a live cluster;
// checking it here catches a rename before CI does.
func TestConsolePage(t *testing.T) {
	x := setup(t, "SPX", false)
	for _, path := range []string{"/", "/anything"} {
		page := x.do("GET", path, "", 200)
		for _, want := range []string{"<title>fake-gpu console</title>", "/api/gpus", "/api/actions/", "</html>"} {
			if !strings.Contains(page, want) {
				t.Fatalf("%s lacks %q", path, want)
			}
		}
	}
	data, err := os.ReadFile("../../test/e2e/demo/dashboard.py")
	if err != nil || !strings.Contains(string(data), "<title>fake-gpu console</title>") {
		t.Fatalf("test/e2e/demo/dashboard.py no longer checks the title: %v", err)
	}
}

func TestActions(t *testing.T) {
	x := setup(t, "SPX", true)
	for _, action := range []string{"crash", "overheat", "ecc-error", "throttle", "retire-page", "xgmi-down", "busy"} {
		if g := x.gpu("POST", "/api/actions/"+action+"?gpu=0"); g.Status == fleet.Healthy && g.Util == 0 {
			t.Fatalf("%s had no effect", action)
		}
		if g := x.gpu("POST", "/api/actions/recover?gpu=0"); g.Status != fleet.Healthy || g.ECCErrors != 0 || g.TargetUtil != 0 {
			t.Fatalf("recover after %s: %+v", action, g)
		}
	}
	// Scripts call actions with GET as well.
	if g := x.gpu("GET", "/api/actions/overheat?gpu=1&temp=99"); g.TempC != 99 {
		t.Fatal(g)
	}
	if g := x.gpu("POST", "/api/actions/idle?gpu=1"); g.Status != fleet.Healthy {
		t.Fatal(g)
	}
	if g := x.gpu("POST", "/api/actions/busy?gpu=2&util=40&mem=10"); g.Util != 40 || g.MemUsedMB != g.MemTotalMB/10 {
		t.Fatal(g)
	}
	if g := x.gpu("POST", "/api/actions/busy?gpu=3&util=400&mem=-1"); g.Util != 85 || g.MemUsedMB != g.MemTotalMB*70/100 {
		t.Fatalf("out of range parameters fall back to defaults: %+v", g)
	}
	if g := x.gpu("POST", "/api/actions/ecc-error?gpu=4&count=-2"); g.ECCErrors != 1 {
		t.Fatal(g)
	}
	if g := x.gpu("POST", "/api/actions/retire-page?gpu=5&count=5000"); g.RetiredPages != 1 {
		t.Fatalf("a count past the bad page limit counts one: %+v", g)
	}
	if g := x.gpu("POST", "/api/actions/retire-page?gpu=6&count=1024"); g.RetiredPages != 1024 {
		t.Fatal(g)
	}

	x.gpu("POST", "/api/actions/crash?gpu=7")
	for _, action := range []string{"throttle", "retire-page", "xgmi-down"} {
		if got := x.do("POST", "/api/actions/"+action+"?gpu=7", "", 409); !strings.Contains(got, "recover it first") {
			t.Fatal(got)
		}
	}
	for _, url := range []string{"/api/actions/crash", "/api/actions/crash?gpu=x", "/api/actions/crash?gpu=-1"} {
		x.do("POST", url, "", 400)
	}
	x.do("POST", "/api/actions/crash?gpu=99", "", 404)
}

func TestGPUResource(t *testing.T) {
	x := setup(t, "SPX", false)
	if g := x.gpu("GET", "/api/gpus/3/"); g.Index != 3 {
		t.Fatal(g)
	}
	if g := x.gpu("POST", "/api/gpus/2"); g.Index != 2 {
		t.Fatal(g)
	}
	x.do("PATCH", "/api/gpus/2", `{"temperature_c":77}`, 200)
	x.do("PUT", "/api/gpus/2", `{"utilization_pct":33}`, 200)
	if g, _ := x.fleet.Get(2); g.TempC != 77 || g.Util != 33 {
		t.Fatal(g)
	}
	x.do("PATCH", "/api/gpus/2", `not json`, 400)
	x.do("PATCH", "/api/gpus/2", `{"temperature_c":"hot"}`, 400)
	x.do("PATCH", "/api/gpus/99", `{}`, 404)
	x.do("GET", "/api/gpus/99", "", 404)
	x.do("GET", "/api/gpus/x", "", 400)
	x.do("GET", "/api/gpus/-1", "", 400)

	var all struct {
		GPUs   []fleet.GPU  `json:"gpus"`
		System fleet.System `json:"system"`
		Trays  []fleet.Tray `json:"trays"`
	}
	if err := json.Unmarshal([]byte(x.do("GET", "/api/gpus", "", 200)), &all); err != nil || len(all.GPUs) != 8 || len(all.Trays) != 1 || all.System.Node != "mock-node" {
		t.Fatalf("%v %+v", err, all)
	}
}

func TestProfilesTraysAndPartitions(t *testing.T) {
	x := setup(t, "SPX", true)
	var catalog []fleet.Summary
	json.Unmarshal([]byte(x.do("GET", "/api/profiles", "", 200)), &catalog)
	if len(catalog) != 7 {
		t.Fatal(catalog)
	}
	var layout map[string]any
	json.Unmarshal([]byte(x.do("GET", "/api/trays/layout", "", 200)), &layout)
	if layout["tray_size"] != 8.0 || layout["can_split"] != false || layout["gpu_count"] != 8.0 {
		t.Fatal(layout)
	}

	for _, step := range []struct {
		mode  string
		total int
	}{{"DPX", 16}, {"CPX", 64}, {"QPX", 32}, {"SPX", 8}} {
		var out struct {
			Mode  string `json:"mode"`
			Total int    `json:"total_gpus"`
		}
		json.Unmarshal([]byte(x.do("POST", "/api/partitions/set?mode="+step.mode, "", 200)), &out)
		if out.Mode != step.mode || out.Total != step.total {
			t.Fatalf("%+v", out)
		}
		if !strings.Contains(x.do("GET", "/api/partitions", "", 200), `"current":"`+step.mode+`"`) {
			t.Fatal("current mode")
		}
	}
	x.do("POST", "/api/partitions/set", "", 400)
	x.do("POST", "/api/partitions/set?mode=TPX", "", 400)

	x.do("POST", "/api/profiles/switch", "", 400)
	x.do("POST", "/api/profiles/switch?profile=nope", "", 404)
	x.do("POST", "/api/profiles/switch?profile=mi250x", "", 200)
	if gpus, _ := x.fleet.All(); len(gpus) != 16 {
		t.Fatal(len(gpus))
	}
	x.do("POST", "/api/profiles/switch-tray?gpu=9&profile=mi210", "", 200)
	x.do("POST", "/api/profiles/switch-tray?gpu=9", "", 400)
	x.do("POST", "/api/profiles/switch-tray?gpu=99&profile=mi210", "", 404)
	if trays := x.fleet.Trays(); trays[1].Profile != "mi210" {
		t.Fatal(trays)
	}
}

// A fixed DPX node keeps its topology until it restarts.
func TestFixedPartitionsRefuseTopologyChanges(t *testing.T) {
	x := setup(t, "DPX", true)
	for _, url := range []string{"/api/partitions/set?mode=SPX", "/api/profiles/switch?profile=mi210", "/api/profiles/switch-tray?gpu=0&profile=mi210"} {
		x.do("POST", url, "", 409)
	}
	if gpus, _ := x.fleet.All(); len(gpus) != 16 {
		t.Fatal("topology changed")
	}
}

func TestMetricsAndHealth(t *testing.T) {
	x := setup(t, "SPX", false)
	x.gpu("POST", "/api/actions/xgmi-down?gpu=1")
	x.gpu("POST", "/api/actions/retire-page?gpu=2&count=3")
	text := x.do("GET", "/metrics", "", 200)
	for _, line := range []string{
		`gpu_temperature{gpu_id="0",card_model="AMD Instinct MI300X",pci_bdf="0000:05:00.0"} 49`,
		`gpu_total_vram{gpu_id="0",card_model="AMD Instinct MI300X",pci_bdf="0000:05:00.0"} 206158430208`,
		`gpu_clock{gpu_id="0",card_model="AMD Instinct MI300X",clock_type="mem"} 1300`,
		`gpu_xgmi_link_up{gpu_id="1",card_model="AMD Instinct MI300X"} 0`,
		`gpu_xgmi_link_up{gpu_id="0",card_model="AMD Instinct MI300X"} 1`,
		`gpu_retired_pages{gpu_id="2",card_model="AMD Instinct MI300X"} 3`,
	} {
		if !strings.Contains(text, line+"\n") {
			t.Fatalf("metrics lack %s", line)
		}
	}
	if n := strings.Count(text, "\n"); n != 8*12 {
		t.Fatalf("%d metric lines, want 12 per GPU", n)
	}
	rec := httptest.NewRecorder()
	x.srv.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Body.String() != "ok" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatal("healthz")
	}
	rec = httptest.NewRecorder()
	x.srv.ServeHTTP(rec, httptest.NewRequest("GET", "/api/system", nil))
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatal("JSON headers")
	}
}
