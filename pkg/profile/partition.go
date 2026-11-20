package profile

import (
	"fmt"
	"strings"
)

// slices is the number of schedulable compute partitions per card for each
// startup mode. Only MI300X DPX (with NPS2 memory) is modelled beyond SPX.
var slices = map[string]int{"SPX": 1, "DPX": 2}

// Partition returns the profile a node reports when its cards boot in mode.
//
// In DPX every card becomes two DRM/KFD devices: they keep the card's PCI
// address and unique ID, because AMD's discovery finds the parent of each
// amdgpu_xcp platform device through them, and get their own index, render
// minor and UUID. Capacity is split evenly. This is what discovery and
// allocation see; there is no execution or isolation behind it.
func Partition(p *Profile, mode string) (*Profile, error) {
	mode = strings.ToUpper(mode)
	n, ok := slices[mode]
	if !ok {
		return nil, fmt.Errorf("invalid partition mode %q: use SPX or DPX", mode)
	}
	out := *p
	out.Devices = append([]Device(nil), p.Devices...)
	out.Card.Partition.Compute = mode
	if n == 1 {
		return &out, nil
	}
	if !strings.Contains(p.Card.Name, "MI300X") {
		return nil, fmt.Errorf("schedulable partitions currently support MI300X only")
	}
	if p.Card.Partition.Compute != "SPX" {
		return nil, fmt.Errorf("partition input must be a physical SPX profile")
	}

	c := &out.Card
	c.Partition.Memory = "NPS2"
	c.Memory.VRAM /= uint64(n)
	c.Memory.VRAMUsed /= uint64(n)
	c.Memory.VisibleVRAM /= uint64(n)
	c.Compute.CUs /= uint32(n)
	c.Compute.SIMDs /= uint32(n)
	c.Compute.XCCs /= uint32(n)
	c.Power.Socket /= uint32(n)
	c.Power.DefaultCap /= uint32(n)
	c.Power.MaxCap /= uint32(n)

	out.Devices = make([]Device, 0, n*len(p.Devices))
	for _, card := range p.Devices {
		for s := range n {
			d := card
			d.Index = len(out.Devices)
			d.RenderMinor = 128 + d.Index
			d.Slice = s
			d.UUID = fmt.Sprintf("%s-part%d", card.UUID, s)
			out.Devices = append(out.Devices, d)
		}
	}
	return &out, nil
}
