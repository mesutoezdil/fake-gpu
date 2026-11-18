package main

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mesutoezdil/fake-gpu/pkg/gpu/kfd"
	"gopkg.in/yaml.v3"
)

func TestCtlDrivesTheRealAPI(t *testing.T) {
	data, err := os.ReadFile("../../profiles/mi300x.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var p kfd.Profile
	if err := yaml.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	state := kfd.NewFleetState(&p, "mi300x")
	server := httptest.NewServer(kfd.NewAPIServer(state, nil))
	defer server.Close()
	call := func(want int, args ...string) string {
		t.Helper()
		var out, errOut bytes.Buffer
		if code := run(append([]string{"-api", server.URL}, args...), &out, &errOut); code != want {
			t.Fatalf("%v: exit %d: %s%s", args, code, out.String(), errOut.String())
		}
		return out.String() + errOut.String()
	}

	if got := call(0, "retire-page", "-gpu", "2", "count=3"); !strings.Contains(got, "GPU 2: page_retired") {
		t.Fatalf("action output %q", got)
	}
	if g, _ := state.GetGPU(2); g.RetiredPages != 3 {
		t.Fatalf("count=3 did not reach the API: %+v", g)
	}
	call(0, "busy", "-gpu", "0", "util=77")
	if g, _ := state.GetGPU(0); g.TargetUtilPct != 77 {
		t.Fatalf("util=77 did not reach the API: %+v", g)
	}
	list := call(0, "list")
	if strings.Count(list, "AMD Instinct MI300X") != 8 || !strings.Contains(list, "page_retired") {
		t.Fatalf("list:\n%s", list)
	}
	if got := call(1, "throttle", "-gpu", "42"); !strings.Contains(got, "404") {
		t.Fatalf("missing GPU: %q", got)
	}
	call(1, "no-such-action", "-gpu", "0")
	call(2, "throttle")
	call(2, "busy", "-gpu", "0", "util")
	call(2)
	server.Close()
	call(1, "list")
}

func TestScenarioRunsFaultsAndChecks(t *testing.T) {
	data, err := os.ReadFile("../../profiles/mi300x.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var p kfd.Profile
	if err := yaml.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	state := kfd.NewFleetState(&p, "mi300x")
	sim := kfd.NewDynamicSimulator(state)
	sim.Start(5)
	defer sim.Stop()
	server := httptest.NewServer(kfd.NewAPIServer(state, nil))
	defer server.Close()

	write := func(body string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "scenario.yaml")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	run := func(want int, body string) string {
		t.Helper()
		var out, errOut bytes.Buffer
		if code := run([]string{"-api", server.URL, "run", write(body)}, &out, &errOut); code != want {
			t.Fatalf("exit %d, want %d:\n%s%s", code, want, out.String(), errOut.String())
		}
		return out.String() + errOut.String()
	}

	// Steps run in time order even when written out of order.
	got := run(0, `
name: load then throttle
steps:
  - {at: 300ms, gpu: 1, do: throttle, expect: {status: throttling, throttled: "true", power_w: "<= 380"}, within: 3s}
  - {at: 0s, gpu: 1, do: busy, args: {util: "90"}, expect: {utilization_pct: ">= 87", power_w: "> 500"}, within: 3s}
  - {at: 600ms, gpu: 1, do: recover, expect: {status: healthy, target_utilization_pct: "0"}, within: 3s}
`)
	if strings.Index(got, "busy -> healthy") > strings.Index(got, "throttle -> throttling") || !strings.Contains(got, "scenario passed") {
		t.Fatalf("steps out of order or not passed:\n%s", got)
	}

	// An expectation that never holds fails the run and says what it saw.
	if got := run(1, `
steps:
  - {at: 0s, gpu: 2, do: retire-page, expect: {retired_pages: ">= 5"}, within: 400ms}
`); !strings.Contains(got, "retired_pages is 1, want >= 5") {
		t.Fatalf("failure message: %s", got)
	}
	run(1, "steps:\n  - {at: 0s, gpu: 0, expect: {no_such_field: \"1\"}}\n")
	run(1, "steps:\n  - {at: 0s, gpu: 0, expect: {status: \"> 3\"}, within: 200ms}\n")
	run(1, "steps: []\n")
	run(1, "steps:\n  - {at: 0s, gpu: 99, do: throttle}\n")

	// The shipped examples must at least parse into steps.
	examples, _ := filepath.Glob("../../examples/scenarios/*.yaml")
	if len(examples) < 2 {
		t.Fatal("example scenarios missing")
	}
	for _, file := range examples {
		raw, _ := os.ReadFile(file)
		var sc scenario
		if err := yaml.Unmarshal(raw, &sc); err != nil || len(sc.Steps) == 0 || sc.Name == "" {
			t.Fatalf("%s: %v %+v", file, err, sc)
		}
	}
}
