//go:build dradiscovery

// Command probe checks fake-gpu against the GPU discovery code in AMD's
// DRA driver (github.com/ROCm/k8s-gpu-dra-driver/pkg/amdgpu), unmodified.
//
//	probe render <profile.yaml> <root>   render the mock sysfs tree under <root>
//	probe check  <profile.yaml>          run AMD's discovery against /sys and
//	                                     compare each device with the profile
//
// AMD's code reads hardcoded /sys paths, so "check" must run with the
// rendered tree mounted at /sys. test/container/dra_discovery/run.sh does that in a
// private mount namespace, and builds this file in GOPATH mode so it can use
// AMD's package without pulling in the driver's whole module (which needs a
// newer Go and the Kubernetes dependency tree). The build tag keeps this file
// out of "go build ./..." in module mode.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mesutoezdil/fake-gpu/pkg/profile"
	"github.com/mesutoezdil/fake-gpu/pkg/sysfs"

	"github.com/ROCm/k8s-gpu-dra-driver/pkg/amdgpu"
)

func main() {
	if len(os.Args) < 3 {
		usage()
	}
	p := loadProfile(os.Args[2])
	if mode := os.Getenv("PARTITION_MODE"); mode != "" {
		var err error
		p, err = profile.Partition(p, mode)
		if err != nil {
			fatalf("partition: %v", err)
		}
	}
	switch os.Args[1] {
	case "render":
		if len(os.Args) != 4 {
			usage()
		}
		if err := sysfs.New(p, os.Args[3]).Render(); err != nil {
			fatalf("render: %v", err)
		}
	case "check":
		if n := check(p); n > 0 {
			fmt.Printf("FAIL: %d mismatch(es)\n", n)
			os.Exit(1)
		}
		fmt.Println("PASS")
	default:
		usage()
	}
}

func check(p *profile.Profile) int {
	fails := 0
	failf := func(format string, a ...any) {
		fails++
		fmt.Printf("  FAIL "+format+"\n", a...)
	}

	// The DRA chart's driver-init container loops until these exist.
	for _, dir := range []string{"/sys/class/kfd", "/sys/module/amdgpu/drivers"} {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			failf("init container would wait forever: %s is not a directory", dir)
		}
	}

	got := amdgpu.GetAMDGPUs()
	if len(got) != len(p.Devices) {
		failf("AMD's discovery found %d devices, profile defines %d", len(got), len(p.Devices))
	}

	d := p.Card
	// AMD's discovery turns the sysfs product_name into an attribute value
	// with this replacer.
	wantName := strings.NewReplacer(" ", "_", "(", "", ")", "").Replace(d.ProductName())

	for _, dev := range p.Devices {
		if !bdfRe.MatchString(dev.BDF) {
			failf("%s: not a lowercase extended BDF; Kubernetes rejects it as pciBusID", dev.BDF)
		}
		key := dev.BDF
		if dev.Slice > 0 {
			key = fmt.Sprintf("amdgpu_xcp_%d", dev.Index)
		}
		g, ok := got[key]
		if !ok {
			failf("%s: not discovered", dev.BDF)
			continue
		}
		wantRoot, wantNUMA := rootComplex(p, dev.BDF)
		if wantRoot == "" {
			failf("%s: not listed under any pcie_topology root complex, so it gets no PCI device, NUMA node or pcieRoot", dev.BDF)
		}
		want := []struct {
			key  string
			want any
		}{
			{"pciAddr", dev.BDF},
			{"card", dev.Index},
			{"renderD", dev.RenderMinor},
			{"deviceID", fmt.Sprintf("0x%04x", d.Device)},
			{"productName", wantName},
			{"driverVersion", p.Software.Driver},
			{"cuCount", d.Compute.CUs},
			{"simdCount", d.Compute.SIMDs},
			{"vramBytes", d.Memory.VRAM},
			{"numaNode", wantNUMA},
			{"computePartitionType", strings.ToLower(d.Partition.Compute)},
			{"memoryPartitionType", strings.ToLower(d.Partition.Memory)},
		}
		for _, w := range want {
			if fmt.Sprint(g[w.key]) != fmt.Sprint(w.want) {
				failf("%s: %s = %q, want %q", dev.BDF, w.key, fmt.Sprint(g[w.key]), fmt.Sprint(w.want))
			}
		}
		root, err := pcieRoot(dev.BDF)
		if err != nil {
			failf("%s: pcieRoot: %v", dev.BDF, err)
		} else if root != wantRoot {
			failf("%s: pcieRoot = %q, want %q", dev.BDF, root, wantRoot)
		}
		fmt.Printf("  ok   %s card%v renderD%v %v cu=%v vram=%v numa=%v root=%s driver=%v\n",
			dev.BDF, g["card"], g["renderD"], g["productName"], g["cuCount"],
			g["vramBytes"], g["numaNode"], root, g["driverVersion"])
	}
	return fails
}

// bdfRe is the format k8s.io/dynamic-resource-allocation/deviceattribute
// accepts for the standard pciBusID attribute.
var bdfRe = regexp.MustCompile(`^([0-9a-f]{4}):([0-9a-f]{2}):([0-9a-f]{2})\.([0-9a-f]{1})$`)

// pcieRoot resolves the PCIe root the way
// k8s.io/dynamic-resource-allocation/deviceattribute.GetPCIeRootAttributeByPCIBusID
// does (that package needs Go 1.25+, so it is mirrored here): follow
// /sys/bus/pci/devices/<BDF> and take the first element under /sys/devices.
func pcieRoot(bdf string) (string, error) {
	sysBusPath := filepath.Join("bus", "pci", "devices", bdf)
	target, err := os.Readlink(filepath.Join("/sys", sysBusPath))
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(target) {
		target = filepath.Clean(filepath.Join(filepath.Dir(sysBusPath), target))
	}
	if !strings.HasPrefix(target, filepath.Join("devices", "pci")) {
		return "", fmt.Errorf("symlink target %q must start with devices/pci", target)
	}
	if filepath.Base(target) != bdf {
		return "", fmt.Errorf("symlink target %q must end with %s", target, bdf)
	}
	return strings.Split(strings.TrimPrefix(target, "devices/"), "/")[0], nil
}

func rootComplex(p *profile.Profile, bdf string) (string, int) {
	if rc, ok := p.Root(bdf); ok {
		return rc.ID, rc.NUMA
	}
	return "", -1
}

func loadProfile(path string) *profile.Profile {
	p, err := profile.Load(path)
	if err != nil {
		fatalf("%v", err)
	}
	return p
}

func usage() {
	fatalf("usage: probe render <profile.yaml> <root> | probe check <profile.yaml>")
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(2)
}
