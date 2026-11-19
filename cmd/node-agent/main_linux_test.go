package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/mesutoezdil/fake-gpu/pkg/profile"
	"golang.org/x/sys/unix"
)

// The kernel's encoding must agree with makedev for every number the agent uses.
func TestMakedevMatchesKernelEncoding(t *testing.T) {
	for _, dev := range [][2]uint32{{234, 0}, {226, 0}, {226, 127}, {226, 128}, {226, 191}} {
		if got, want := makedev(dev[0], dev[1]), unix.Mkdev(dev[0], dev[1]); got != want {
			t.Errorf("makedev(%d, %d) = %#x, unix.Mkdev = %#x", dev[0], dev[1], got, want)
		}
	}
}

// Needs root for mknod; CI runs it with sudo next to the renderer restart test.
func TestCreateDeviceNodes(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("mknod needs root")
	}
	root := t.TempDir()
	profile := &profile.Profile{Devices: []profile.Device{{Index: 0, RenderMinor: 128}, {Index: 1, RenderMinor: 129}}}
	// A stale plain file where a node belongs is replaced, not kept.
	if err := os.MkdirAll(filepath.Join(root, "dev/dri"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dev/dri/renderD128"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := createDeviceNodes(profile, root); err != nil {
		t.Fatal(err)
	}
	want := map[string][2]uint32{"kfd": {234, 0}}
	for _, d := range profile.Devices {
		want[fmt.Sprintf("dri/renderD%d", d.RenderMinor)] = [2]uint32{226, uint32(d.RenderMinor)}
		want[fmt.Sprintf("dri/card%d", d.Index)] = [2]uint32{226, uint32(d.Index)}
	}
	for rel, dev := range want {
		var st unix.Stat_t
		if err := unix.Stat(filepath.Join(root, "dev", rel), &st); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if st.Mode&unix.S_IFMT != unix.S_IFCHR {
			t.Errorf("%s is not a character device", rel)
		}
		if major, minor := unix.Major(uint64(st.Rdev)), unix.Minor(uint64(st.Rdev)); major != dev[0] || minor != dev[1] {
			t.Errorf("%s = %d:%d, want %d:%d", rel, major, minor, dev[0], dev[1])
		}
	}
}
