package main

import (
	"bytes"
	"net/http/httptest"
	"os"
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
