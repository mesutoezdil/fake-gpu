package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mesutoezdil/fake-gpu/pkg/fleet"
	"github.com/mesutoezdil/fake-gpu/pkg/profile"
	"github.com/mesutoezdil/fake-gpu/pkg/sysfs"
	"gopkg.in/yaml.v3"
)

func TestCLIReportsAgentState(t *testing.T) {
	data, err := os.ReadFile("../../profiles/mi300x.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var p profile.Profile
	if err := yaml.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := sysfs.New(&p, root).Render(); err != nil {
		t.Fatal(err)
	}
	g, _ := fleet.New(&p, "mi300x").Get(1)
	g.Status, g.Throttled, g.RetiredPages, g.TempC = "throttling", true, 3, 77
	if err := sysfs.New(&p, root).SMI(&g); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOCK_AMDSMI_STATE_DIR", filepath.Join(root, "smi"))
	call := func(want int, args ...string) string {
		t.Helper()
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != want {
			t.Fatalf("%v: exit %d: %s", args, code, errOut.String())
		}
		return out.String()
	}

	if n := strings.Count(call(0, "amd-smi", "list"), "GPU: "); n != len(p.Devices) {
		t.Fatalf("list shows %d GPUs, want %d", n, len(p.Devices))
	}
	text := call(0, "amd-smi", "metric", "--gpu", "1")
	for _, want := range []string{"GPU: 1\n", "EDGE: 77 °C", "THROTTLE_STATUS: THROTTLED", "STATUS: throttling"} {
		if !strings.Contains(text, want) {
			t.Fatalf("metric output lacks %q:\n%s", want, text)
		}
	}
	var rows []struct {
		GPU     int
		Retired int
	}
	if err := json.Unmarshal([]byte(call(0, "amd-smi", "bad-pages", "--json", "--gpu", "1")), &rows); err != nil || len(rows) != 1 || rows[0].GPU != 1 || rows[0].Retired != 3 {
		t.Fatalf("bad-pages json: %v %+v", err, rows)
	}
	if table := call(0, "/usr/bin/rocm-smi"); !strings.Contains(table, "GPU_TEMP") || !strings.Contains(table, "throttling") {
		t.Fatalf("rocm-smi table:\n%s", table)
	}
	call(1, "amd-smi", "list", "--gpu", "99")
	call(2, "amd-smi", "nonsense")
	t.Setenv("MOCK_AMDSMI_STATE_DIR", filepath.Join(root, "missing"))
	call(1, "amd-smi", "list")
}
