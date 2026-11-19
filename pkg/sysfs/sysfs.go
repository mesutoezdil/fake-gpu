// Package sysfs renders the files an amdgpu node exposes, under a root
// directory the mock's consumers mount in place of /sys and /dev: the KFD
// topology, DRM cards with hwmon, the amdgpu module and its bound PCI
// devices, and the AMD SMI state. Runtime faults and telemetry are written
// back into the same tree.
package sysfs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/mesutoezdil/fake-gpu/pkg/fleet"
	"github.com/mesutoezdil/fake-gpu/pkg/profile"
	"github.com/mesutoezdil/fake-gpu/pkg/smistate"
)

// Backend provides openable device nodes instead of placeholders.
type Backend interface {
	Reconcile(*profile.Profile) error
	RestoreDevice(index int) error
	RemoveDevice(index int) error
}

// Tree is the rendered tree of one node.
type Tree struct {
	root    string
	backend Backend
	hostDev string // where crash and recovery manage card and render nodes; empty: nowhere
	klog    *kernelLog

	mu      sync.Mutex
	prof    *profile.Profile
	xgmiOff map[int]bool // card indices whose xGMI links are down
}

// New prepares a tree for p under root; nothing is written yet.
func New(p *profile.Profile, root string) *Tree {
	return &Tree{prof: p, root: root, klog: &kernelLog{}, xgmiOff: map[int]bool{}}
}

// SetBackend makes device nodes come from b.
func (t *Tree) SetBackend(b Backend) { t.backend = b }

// SetHostDev lets crash and recovery remove and recreate card and render
// nodes in dir, normally /dev. Without it the tree stays below its root,
// which is what tests and unprivileged runs want.
func (t *Tree) SetHostDev(dir string) { t.hostDev = dir }

// SetKernelLog appends a line per fault transition to path. /dev/kmsg puts
// them in the node's real kernel log, so it is opt-in.
func (t *Tree) SetKernelLog(path string) { t.klog.path = path }

// Profile is the profile the tree currently renders.
func (t *Tree) Profile() *profile.Profile {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.prof
}

// Root is the directory the tree is rendered under.
func (t *Tree) Root() string { return t.root }

// Render writes every surface, device nodes included.
func (t *Tree) Render() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.layout(); err != nil {
		return err
	}
	if err := t.devices(); err != nil {
		return fmt.Errorf("device nodes: %w", err)
	}
	return t.smi()
}

// Switch renders another profile in place. Device node paths do not depend
// on the profile; a backend reconciles its own.
func (t *Tree) Switch(p *profile.Profile) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prof = p
	t.xgmiOff = map[int]bool{}
	t.klog.reset()
	if err := t.layout(); err != nil {
		return err
	}
	if t.backend != nil {
		if err := t.backend.Reconcile(p); err != nil {
			return err
		}
	}
	return t.smi()
}

// layout replaces sys/ with the static tree of the profile. The sys
// directory itself is kept: consumers have it bind-mounted.
func (t *Tree) layout() error {
	l, err := build(t.prof)
	if err != nil {
		return err
	}
	if err := clear(t.path("sys")); err != nil {
		return err
	}
	return t.write(l)
}

func (t *Tree) smi() error {
	return smistate.Sync(t.path("smi"), fleet.FromProfile(t.prof, ""))
}

func (t *Tree) path(rel string) string { return filepath.Join(t.root, rel) }

// write puts a layout on disk, symlinks relative so the tree resolves
// wherever it is mounted.
func (t *Tree) write(l *layout) error {
	for _, rel := range sorted(l.files) {
		if err := writeFile(t.path(rel), l.files[rel]); err != nil {
			return err
		}
	}
	for _, rel := range sorted(l.links) {
		link := t.path(rel)
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			return err
		}
		target, err := filepath.Rel(filepath.Dir(link), t.path(l.links[rel]))
		if err != nil {
			return err
		}
		os.Remove(link)
		if err := os.Symlink(target, link); err != nil {
			return err
		}
	}
	return nil
}

func sorted(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// devices creates /dev/kfd and the render nodes. Without a backend they are
// placeholders; the agent replaces them with character devices when root.
func (t *Tree) devices() error {
	if t.backend != nil {
		return t.backend.Reconcile(t.prof)
	}
	paths := []string{t.path("dev/kfd")}
	for _, d := range t.prof.Devices {
		paths = append(paths, t.path(fmt.Sprintf("dev/dri/renderD%d", d.RenderMinor)))
	}
	for _, p := range paths {
		if err := placeholder(p); err != nil {
			return err
		}
	}
	return nil
}

// placeholder leaves an existing character device alone: one survives an
// unclean agent shutdown, and opening it without a driver fails with ENXIO.
func placeholder(path string) error {
	info, err := os.Lstat(path)
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return err
	case info.Mode()&os.ModeCharDevice != 0:
		return nil
	case !info.Mode().IsRegular():
		return fmt.Errorf("unexpected device placeholder type at %s: %s", path, info.Mode())
	}
	return writeFile(path, "")
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// replace writes through a temporary file and a rename, so a reader never
// sees half a value.
func replace(path, content string) error {
	tmp := path + ".tmp"
	if err := writeFile(tmp, content); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// clear empties dir and keeps its inode.
func clear(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
