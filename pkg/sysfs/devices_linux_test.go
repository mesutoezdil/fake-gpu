package sysfs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/mesutoezdil/fake-gpu/pkg/profile"
)

// After an unclean agent shutdown the character devices the agent made are
// still there. Rendering again must keep them, not replace them with files.
func TestRenderDeviceNodesAfterUncleanShutdown(t *testing.T) {
	tree := New(&profile.Profile{Devices: []profile.Device{{RenderMinor: 128}}}, t.TempDir())
	if err := tree.devices(); err != nil {
		t.Fatal(err)
	}
	paths := []string{filepath.Join(tree.Root(), "dev/kfd"), filepath.Join(tree.Root(), "dev/dri/renderD128")}
	before := make([]os.FileInfo, len(paths))
	for i, path := range paths {
		os.Remove(path)
		if err := syscall.Mknod(path, syscall.S_IFCHR|0o600, 234<<8); err != nil {
			if err == syscall.EPERM {
				t.Skip("needs CAP_MKNOD; CI runs it under sudo")
			}
			t.Fatal(err)
		}
		before[i], _ = os.Lstat(path)
	}
	for range 2 {
		if err := tree.devices(); err != nil {
			t.Fatal(err)
		}
	}
	for i, path := range paths {
		after, err := os.Lstat(path)
		if err != nil || after.Mode()&os.ModeCharDevice == 0 || !os.SameFile(before[i], after) {
			t.Fatalf("%s replaced", path)
		}
	}
}
