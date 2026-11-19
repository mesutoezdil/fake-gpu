// node-agent renders a node's mock AMD GPUs, serves their API and console,
// and keeps their telemetry moving until it is told to stop.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mesutoezdil/fake-gpu/pkg/cdi"
	"github.com/mesutoezdil/fake-gpu/pkg/console"
	"github.com/mesutoezdil/fake-gpu/pkg/fleet"
	"github.com/mesutoezdil/fake-gpu/pkg/mockdriver"
	"github.com/mesutoezdil/fake-gpu/pkg/profile"
	"github.com/mesutoezdil/fake-gpu/pkg/sysfs"
)

type config struct {
	profile, profiles, root, api, partition, backend, kernelLog, cuseDev string
}

func main() {
	var c config
	flag.StringVar(&c.profile, "config", "/etc/fake-gpu/config.yaml", "path to GPU profile config")
	flag.StringVar(&c.profiles, "profiles", "/etc/fake-gpu/profiles", "directory containing all GPU profile YAML files")
	flag.StringVar(&c.root, "root", "/var/lib/fake-gpu", "root directory for staged surfaces")
	flag.StringVar(&c.api, "api", ":8080", "address for the status/control API and dashboard")
	flag.StringVar(&c.partition, "partition", "SPX", "fixed startup compute partition: SPX or MI300X DPX/NPS2")
	flag.StringVar(&c.backend, "device-backend", "nodes", "device backend: nodes or cuse (requires optional driver image)")
	flag.StringVar(&c.kernelLog, "kernel-log", "", "append a line per fault transition to this file; /dev/kmsg writes to the real kernel log")
	flag.StringVar(&c.cuseDev, "cuse-host-dev", "/dev", "host device directory for the CUSE backend")
	flag.Parse()
	if err := run(c); err != nil {
		log.Fatal(err)
	}
}

func run(c config) error {
	if c.backend != "nodes" && c.backend != "cuse" {
		return fmt.Errorf("device-backend must be nodes or cuse")
	}
	log.Printf("fake-gpu node agent starting: config %s, root %s", c.profile, c.root)
	boot, err := profile.Load(c.profile)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	p, err := profile.Partition(boot, c.partition)
	if err != nil {
		return fmt.Errorf("partition configuration: %w", err)
	}
	log.Printf("profile: %s (%d GPUs, %s)", p.Card.Name, len(p.Devices), p.Card.Architecture)

	tree := sysfs.New(p, c.root)
	tree.SetKernelLog(c.kernelLog)
	var provider *mockdriver.Provider
	if c.backend == "cuse" {
		if provider, err = mockdriver.New(c.root, c.cuseDev, "mock-device"); err != nil {
			return err
		}
		defer provider.Close()
		tree.SetBackend(provider)
	}
	if err := tree.Render(); err != nil {
		return fmt.Errorf("rendering surfaces: %w", err)
	}
	if err := stage(c.root, provider != nil); err != nil {
		return err
	}
	log.Printf("surfaces staged at %s: sys/class/kfd, sys/module/amdgpu, sys/bus/pci, dev, smi", c.root)
	writeCDI(p, c.root)
	if os.Getuid() == 0 && provider == nil {
		// kubelet mounts /dev/kfd and the render nodes the device plugin
		// names, so they must exist on the host as well as in the root.
		tree.SetHostDev("/dev")
		for _, dir := range []string{c.root, ""} {
			if err := createDeviceNodes(p, dir); err != nil {
				log.Printf("warning: device nodes in %q: %v", dir+"/dev", err)
			}
		}
	}

	f := gpus(p, c)
	sim := fleet.NewSimulator(f)
	sim.OnTick = func(g *fleet.GPU) {
		if err := tree.SMI(g); err != nil {
			log.Printf("warning: AMD SMI state sync: %v", err)
		}
		if err := tree.Telemetry(g); err != nil {
			log.Printf("warning: sysfs telemetry sync: %v", err)
		}
	}
	// HIP clients publish their allocations here, as any user.
	hip := filepath.Join(c.root, "hip")
	if err := os.MkdirAll(hip, 0o1777); err == nil {
		os.Chmod(hip, 0o1777)
	}
	sim.TrackHIP(c.root)
	sim.Start(time.Second)
	defer sim.Stop()

	go func() {
		if err := console.New(f, tree).ListenAndServe(c.api); err != nil {
			log.Printf("API server error: %v", err)
		}
	}()
	log.Printf("dashboard: http://localhost%s", c.api)
	return wait(provider)
}

// gpus boots the fleet. Helm mounts the selected profile as config.yaml, so
// the catalog entry with the same model names it; otherwise the file does.
func gpus(p *profile.Profile, c config) *fleet.Fleet {
	slug := profileSlug(c.profile)
	catalog, err := profile.LoadDir(c.profiles)
	if err != nil {
		log.Printf("warning: loading profile catalog from %s: %v", c.profiles, err)
		return fleet.New(p, slug)
	}
	if filepath.Base(c.profile) == "config.yaml" {
		for _, s := range catalog.Slugs {
			if catalog.Profiles[s].Card.Name == p.Card.Name {
				slug = s
				break
			}
		}
	}
	f := fleet.New(p, slug)
	f.AddCatalog(catalog)
	log.Printf("loaded %d profiles from %s", len(catalog.Slugs), c.profiles)
	return f
}

// profileSlug is the profile's file name without extension, "mi300x" for
// ".../mi300x.yaml". config.yaml counts as mi300x until the catalog says
// otherwise.
func profileSlug(path string) string {
	slug := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if slug == "config" {
		return "mi300x"
	}
	return slug
}

// stage copies the mock libraries to the root, where workload containers
// mount them from. The CUSE backend also needs its libdrm.
func stage(root string, cuse bool) error {
	lib := filepath.Join(root, "driver/usr/lib64")
	if err := copyFile("/usr/lib64/libamd_smi.so", filepath.Join(lib, "libamd_smi.so")); err != nil {
		log.Printf("warning: mock AMD SMI library not staged: %v", err)
	} else {
		os.Symlink("libamd_smi.so", filepath.Join(lib, "libamd_smi.so.27"))
	}
	// The HIP shim is optional: images without it run fine.
	copyFile("/usr/lib64/libfakehip.so", filepath.Join(lib, "libfakehip.so"))
	copyFile("/usr/include/fakehip.h", filepath.Join(root, "driver/usr/include/fakehip.h"))
	if cuse {
		return copyFile("/opt/fake-gpu/lib/libdrm_amdgpu.so.1", filepath.Join(lib, "libdrm_amdgpu.so.1"))
	}
	return nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o755)
}

// writeCDI puts the CDI spec in the root and where container runtimes look:
// /etc/cdi for amd-container-runtime, /var/run/cdi as the fallback.
func writeCDI(p *profile.Profile, root string) {
	spec := cdi.Generate(p, root)
	for _, path := range []string{filepath.Join(root, "cdi/amd.json"), "/etc/cdi/amd.json", "/var/run/cdi/amd.json"} {
		if err := spec.Write(path); err != nil {
			log.Printf("warning: CDI spec %s: %v", path, err)
		} else {
			log.Printf("CDI spec %s (%d devices)", path, len(spec.Devices))
		}
	}
}

// wait blocks until a stop signal or a backend failure. The staged tree is
// left in place: exporters and allocators have its directories mounted, and
// the next agent reconciles it.
func wait(provider *mockdriver.Provider) error {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	var failed <-chan error
	if provider != nil {
		failed = provider.Errors
	}
	select {
	case s := <-sig:
		log.Printf("received %v; keeping staged surfaces for mounted consumers", s)
		return nil
	case err := <-failed:
		return fmt.Errorf("device backend failure: %w", err)
	}
}

// createDeviceNodes makes /dev/kfd and the card and render nodes under
// root/dev, or /dev itself for an empty root.
func createDeviceNodes(p *profile.Profile, root string) error {
	if root == "" {
		root = "/"
	}
	dev := filepath.Join(root, "dev")
	if err := os.MkdirAll(filepath.Join(dev, "dri"), 0o755); err != nil {
		return err
	}
	if err := mknod(filepath.Join(dev, "kfd"), 234, 0); err != nil {
		return err
	}
	for _, d := range p.Devices {
		if err := mknod(filepath.Join(dev, fmt.Sprintf("dri/renderD%d", d.RenderMinor)), 226, uint32(d.RenderMinor)); err != nil {
			return err
		}
		if err := mknod(filepath.Join(dev, fmt.Sprintf("dri/card%d", d.Index)), 226, uint32(d.Index)); err != nil {
			log.Printf("warning: %v", err)
		}
	}
	return nil
}

// mknod replaces whatever is at path with a character device.
func mknod(path string, major, minor uint32) error {
	os.Remove(path)
	if err := syscall.Mknod(path, syscall.S_IFCHR|0o660, int(makedev(major, minor))); err != nil {
		return fmt.Errorf("mknod %s: %w", path, err)
	}
	return nil
}

func makedev(major, minor uint32) uint64 { return uint64(major)<<8 | uint64(minor) }
