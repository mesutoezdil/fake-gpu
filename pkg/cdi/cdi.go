// Package cdi writes the Container Device Interface spec for the mock GPUs,
// in the shape AMD's amd-ctk (ROCm/container-toolkit) generates: an "all"
// device plus one device per GPU index, each with /dev/kfd and its render
// node, and ROCR_VISIBLE_DEVICES saying which GPUs the ROCm runtime sees.
package cdi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/mesutoezdil/fake-gpu/pkg/profile"
)

type Spec struct {
	Version string   `json:"cdiVersion"`
	Kind    string   `json:"kind"`
	Devices []Device `json:"devices"`
}

type Device struct {
	Name  string `json:"name"`
	Edits Edits  `json:"containerEdits"`
}

type Edits struct {
	Nodes []Node   `json:"deviceNodes,omitempty"`
	Env   []string `json:"env,omitempty"`
}

type Node struct {
	Path        string `json:"path"`
	HostPath    string `json:"hostPath"`
	Permissions string `json:"permissions,omitempty"`
}

// Generate builds the spec for p with device nodes staged under root.
func Generate(p *profile.Profile, root string) *Spec {
	node := func(rel string) Node {
		return Node{Path: "/" + rel, HostPath: filepath.Join(root, rel), Permissions: "rw"}
	}
	render := func(d profile.Device) Node { return node(fmt.Sprintf("dev/dri/renderD%d", d.RenderMinor)) }
	kfd := node("dev/kfd")

	all := Device{Name: "all", Edits: Edits{Nodes: []Node{kfd}, Env: []string{"ROCR_VISIBLE_DEVICES=all"}}}
	for _, d := range p.Devices {
		all.Edits.Nodes = append(all.Edits.Nodes, render(d))
	}
	spec := &Spec{Version: "0.7.0", Kind: "amd.com/gpu", Devices: []Device{all}}
	for _, d := range p.Devices {
		spec.Devices = append(spec.Devices, Device{
			Name:  strconv.Itoa(d.Index),
			Edits: Edits{Nodes: []Node{kfd, render(d)}, Env: []string{fmt.Sprintf("ROCR_VISIBLE_DEVICES=%d", d.Index)}},
		})
	}
	return spec
}

// Write stores the spec as indented JSON.
func (s *Spec) Write(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling CDI spec: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}
