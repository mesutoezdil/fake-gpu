package cdi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mesutoezdil/fake-gpu/pkg/profile"
)

func TestSpec(t *testing.T) {
	p, err := profile.Load("../../profiles/mi300x.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec := Generate(p, "/var/lib/fake-gpu")
	if spec.Version != "0.7.0" || spec.Kind != "amd.com/gpu" || len(spec.Devices) != 9 {
		t.Fatalf("%+v", spec)
	}
	all := spec.Devices[0]
	if all.Name != "all" || len(all.Edits.Nodes) != 9 || all.Edits.Env[0] != "ROCR_VISIBLE_DEVICES=all" {
		t.Fatalf("%+v", all)
	}
	one := spec.Devices[4]
	if one.Name != "3" || one.Edits.Env[0] != "ROCR_VISIBLE_DEVICES=3" ||
		one.Edits.Nodes[0] != (Node{"/dev/kfd", "/var/lib/fake-gpu/dev/kfd", "rw"}) ||
		one.Edits.Nodes[1] != (Node{"/dev/dri/renderD131", "/var/lib/fake-gpu/dev/dri/renderD131", "rw"}) {
		t.Fatalf("%+v", one)
	}

	path := filepath.Join(t.TempDir(), "cdi/amd.json")
	if err := spec.Write(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var back Spec
	if err := json.Unmarshal(data, &back); err != nil || len(back.Devices) != 9 || !strings.Contains(string(data), `"containerEdits"`) {
		t.Fatalf("%v %s", err, data)
	}
	if err := spec.Write("/dev/null/x"); err == nil {
		t.Fatal("unwritable path accepted")
	}
}
