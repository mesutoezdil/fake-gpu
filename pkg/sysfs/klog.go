package sysfs

import (
	"fmt"
	"os"
	"sync"

	"github.com/mesutoezdil/fake-gpu/pkg/fleet"
)

// kernelLog records fault transitions the way amdgpu logs them. Where the
// driver has a message for an event the line uses its format (from the 7.0
// module) so log parsers match; other events go under a fake-gpu tag rather
// than an invented driver line.
type kernelLog struct {
	path string

	mu   sync.Mutex
	last map[int]fleet.Status // by GPU index, to log changes only
	seq  uint64               // event number in RAS lines
}

func (k *kernelLog) reset() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.last = nil
}

func (k *kernelLog) transition(g *fleet.GPU) {
	k.mu.Lock()
	last, seen := k.last[g.Index]
	if k.last == nil {
		k.last = map[int]fleet.Status{}
	}
	k.last[g.Index] = g.Status
	k.seq++
	seq := k.seq
	k.mu.Unlock()
	if !seen {
		last = fleet.Healthy
	}
	if k.path == "" || last == g.Status {
		return
	}
	line := fmt.Sprintf("amdgpu %s: %s\n", g.BDF, event(g, last, seq))
	if k.path == "/dev/kmsg" {
		line = "<3>" + line // KERN_ERR
	}
	f, err := os.OpenFile(k.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(line)
}

func event(g *fleet.GPU, last fleet.Status, seq uint64) string {
	ras := func(count int, block string) string {
		return fmt.Sprintf("amdgpu: {%d} socket: 0, die: 0, %d new uncorrectable hardware errors detected in %s block", seq, count, block)
	}
	switch g.Status {
	case fleet.Healthy:
		if last == fleet.Crashed {
			return "amdgpu: GPU reset(1) succeeded!"
		}
		return "fake-gpu: recovered"
	case fleet.Crashed:
		return "fake-gpu: device lost"
	case fleet.Overheating:
		return "amdgpu: ERROR: GPU over temperature range(SW CTF) detected!"
	case fleet.ECCError:
		return ras(g.ECCErrors, "umc")
	case fleet.Throttling:
		return "amdgpu: WARN: GPU thermal throttling temperature reached, expect performance decrease. TEMP_HOTSPOT."
	case fleet.PageRetired:
		return fmt.Sprintf("fake-gpu: %d VRAM page(s) retired", g.RetiredPages)
	case fleet.XGMIDown:
		return ras(1, "xgmi_wafl")
	}
	return "fake-gpu: status " + string(g.Status)
}
