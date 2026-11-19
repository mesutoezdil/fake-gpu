package sysfs

import (
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/mesutoezdil/fake-gpu/pkg/fleet"
	"github.com/mesutoezdil/fake-gpu/pkg/profile"
	"github.com/mesutoezdil/fake-gpu/pkg/smistate"
)

// MaxBadPages bounds the retired-page list of one GPU.
const MaxBadPages = 1024

// device returns the rendered device behind a GPU of the API. Entries of the
// console's virtual partition modes that are not one of the profile's own
// devices have none: they are display state and touch nothing here.
func (t *Tree) device(g *fleet.GPU) (pos int, d profile.Device, ok bool) {
	for pos, d := range t.prof.Devices {
		if d.Index == g.Index && d.BDF == g.BDF && d.Slice == g.Slice {
			return pos, d, true
		}
	}
	return 0, profile.Device{}, false
}

// SMI writes the AMD SMI snapshot of one GPU.
func (t *Tree) SMI(g *fleet.GPU) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, _, ok := t.device(g); !ok {
		return nil
	}
	return smistate.Write(t.path("smi"), g)
}

// Telemetry refreshes the live sensors of one GPU. It is cheap enough for
// every simulator tick.
func (t *Tree) Telemetry(g *fleet.GPU) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, _, ok := t.device(g); !ok {
		return nil
	}
	return t.telemetry(g)
}

func (t *Tree) telemetry(g *fleet.GPU) error {
	dir := t.path(card(g.Index))
	clocks := t.prof.Card.Clocks
	// Hotspot and memory run 6 above and 4 below edge, as in libamd_smi.so.
	for name, v := range map[string]string{
		"gpu_busy_percent":            fmt.Sprint(g.Util),
		"mem_info_vram_used":          fmt.Sprint(uint64(g.MemUsedMB) << 20),
		"mem_info_vis_vram_used":      fmt.Sprint(uint64(g.MemUsedMB) << 20),
		"pp_dpm_sclk":                 dpm(clocks.GFXMin, clocks.GFXMax, uint32(g.GFXClockMHz)),
		"hwmon/hwmon0/temp1_input":    fmt.Sprint(g.TempC * 1000),
		"hwmon/hwmon0/temp2_input":    fmt.Sprint(max(g.TempC+6, 0) * 1000),
		"hwmon/hwmon0/temp3_input":    fmt.Sprint(max(g.TempC-4, 0) * 1000),
		"hwmon/hwmon0/power1_average": fmt.Sprint(uint64(g.PowerW) * 1_000_000),
		"hwmon/hwmon0/freq1_input":    fmt.Sprint(uint64(g.GFXClockMHz) * 1_000_000),
		"hwmon/hwmon0/freq2_input":    fmt.Sprint(uint64(g.MemClockMHz) * 1_000_000),
	} {
		if err := replace(dir+"/"+name, v+"\n"); err != nil {
			return err
		}
	}
	return nil
}

// Apply writes the state of one GPU everywhere it shows: SMI, RAS, retired
// pages, xGMI links, sensors, and for a crash the driver binding and nodes.
func (t *Tree) Apply(g *fleet.GPU) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	pos, d, ok := t.device(g)
	if !ok {
		return nil
	}
	t.klog.transition(g)
	if err := smistate.Write(t.path("smi"), g); err != nil {
		return err
	}

	ras := node(pos+1) + "/ras"
	fatal := "0"
	if g.Status == fleet.Crashed || g.ECCErrors > 0 {
		fatal = "1"
	}
	if err := writeFile(t.path(ras+"/fatal_error"), fatal+"\n"); err != nil {
		return err
	}
	if err := writeFile(t.path(ras+"/ecc_uncorrectable"), fmt.Sprintf("%d\n", g.ECCErrors)); err != nil {
		return err
	}
	// amdgpu lists retired pages as "pfn : size : flags"; R is reserved.
	var pages strings.Builder
	for i := range min(g.RetiredPages, MaxBadPages) {
		fmt.Fprintf(&pages, "0x%08x : 0x00001000 : R\n", 0x1000+i)
	}
	if err := writeFile(t.path(card(g.Index)+"/ras/gpu_vram_bad_pages"), pages.String()); err != nil {
		return err
	}
	if err := t.setXGMI(g.Index, g.Status == fleet.XGMIDown); err != nil {
		return err
	}
	if err := t.telemetry(g); err != nil {
		return err
	}
	if g.Status == fleet.Crashed {
		return t.unplug(d)
	}
	return t.replug(d)
}

// binding is the directory whose presence tells AMD's device plugin and DRA
// driver that amdgpu drives the device.
func (t *Tree) binding(d profile.Device) string {
	switch {
	case d.Slice > 0:
		return xcp(d.Index)
	case t.prof.Card.Partition.Compute == "DPX":
		return bound + "/" + d.BDF + "/drm" // keep the parent while its sibling lives
	}
	return bound + "/" + d.BDF
}

func (t *Tree) nodes(d profile.Device) (render, cardNode string) {
	return fmt.Sprintf("%s/dri/renderD%d", t.hostDev, d.RenderMinor), fmt.Sprintf("%s/dri/card%d", t.hostDev, d.Index)
}

// unplug makes a crashed device disappear from discovery and from /dev.
func (t *Tree) unplug(d profile.Device) error {
	switch {
	case t.backend != nil:
		if err := t.backend.RemoveDevice(d.Index); err != nil {
			return err
		}
	case t.hostDev != "":
		render, cardNode := t.nodes(d)
		os.Remove(render)
		os.Remove(cardNode)
	}
	return os.RemoveAll(t.path(t.binding(d)))
}

// replug brings a recovered device back.
func (t *Tree) replug(d profile.Device) error {
	switch {
	case t.backend != nil:
		if err := t.backend.RestoreDevice(d.Index); err != nil {
			return err
		}
	case t.hostDev != "":
		render, cardNode := t.nodes(d)
		mknod(render, d.RenderMinor)
		mknod(cardNode, d.Index)
	}
	if _, err := os.Stat(t.path(t.binding(d))); !os.IsNotExist(err) {
		return nil
	}
	l := newLayout()
	bind(l, t.prof, d)
	return t.write(l)
}

// mknod creates a DRM character device unless something is there.
func mknod(path string, minor int) {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		syscall.Mknod(path, syscall.S_IFCHR|0o660, 226<<8|minor)
	}
}

// setXGMI marks the xGMI links of a card down or up. A link is up only
// while both of its ends are, so recovering one card leaves its links to a
// faulted peer down.
func (t *Tree) setXGMI(index int, down bool) error {
	p := t.prof
	if p.XGMI.Hive == "" || t.xgmiOff[index] == down {
		return nil
	}
	t.xgmiOff[index] = down
	flag := "0"
	if down {
		flag = "1"
	}
	if err := writeFile(t.path(card(index)+"/xgmi_error"), flag+"\n"); err != nil {
		return err
	}
	for pos, d := range p.Devices {
		for i, peer := range peers(p, d) {
			up := !t.xgmiOff[d.Index] && !t.xgmiOff[peer.Index]
			path := fmt.Sprintf("%s/io_links/%d/properties", node(pos+1), i+1)
			if err := writeFile(t.path(path), xgmiLink(p, d, peer, up)); err != nil {
				return err
			}
		}
	}
	return nil
}
