// Package smistate is the file format between the agent and the AMD SMI
// mock (pkg/mocksmi) and amd-smi CLI: a count file and one small text file
// per GPU, replaced atomically so a reader never sees a torn snapshot.
//
//	<temp C> <power W> <cap W> <util %> <used MiB> <total MiB> <ecc> <gfx MHz> <mem MHz>
//	<name>
//	<uuid>
//	<pci bdf>
//	<status> <throttled 0|1> <retired pages>
//
// libamd_smi.so reads the first four lines; the fifth is for the CLI.
package smistate

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mesutoezdil/fake-gpu/pkg/fleet"
)

// Write replaces the snapshot of one GPU.
func Write(dir string, g *fleet.GPU) error {
	throttled := 0
	if g.Throttled {
		throttled = 1
	}
	text := fmt.Sprintf("%d %d %d %d %d %d %d %d %d\n%s\n%s\n%s\n%s %d %d\n",
		g.TempC, g.PowerW, g.PowerCapW, g.Util, g.MemUsedMB, g.MemTotalMB, g.ECCErrors, g.GFXClockMHz, g.MemClockMHz,
		g.Name, UUID(g.UUID), g.BDF, g.Status, throttled, g.RetiredPages)
	return replace(dir, fmt.Sprintf("gpu%d", g.Index), text)
}

// Sync makes dir hold exactly gpus. The directory itself is kept: collectors
// have it bind-mounted, and a new inode would strand them.
func Sync(dir string, gpus []fleet.GPU) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for i := range gpus {
		if err := Write(dir, &gpus[i]); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		n, ok := strings.CutPrefix(e.Name(), "gpu")
		if i, err := strconv.Atoi(n); ok && err == nil && i >= len(gpus) {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return replace(dir, "count", fmt.Sprintf("%d\n", len(gpus)))
}

// Read loads the snapshots of every GPU dir counts.
func Read(dir string) ([]fleet.GPU, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "count"))
	if err != nil {
		return nil, err
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("smi count: %w", err)
	}
	gpus := make([]fleet.GPU, count)
	for i := range gpus {
		if err := parse(dir, i, &gpus[i]); err != nil {
			return nil, fmt.Errorf("gpu%d: %w", i, err)
		}
	}
	return gpus, nil
}

func parse(dir string, i int, g *fleet.GPU) error {
	data, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("gpu%d", i)))
	if err != nil {
		return err
	}
	rows := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(rows) < 4 {
		return fmt.Errorf("truncated snapshot")
	}
	*g = fleet.GPU{Index: i, Name: rows[1], UUID: rows[2], BDF: rows[3], Status: fleet.Healthy}
	if _, err := fmt.Sscan(rows[0], &g.TempC, &g.PowerW, &g.PowerCapW, &g.Util, &g.MemUsedMB, &g.MemTotalMB, &g.ECCErrors, &g.GFXClockMHz, &g.MemClockMHz); err != nil {
		return err
	}
	if len(rows) > 4 {
		var throttled int
		if _, err := fmt.Sscan(rows[4], &g.Status, &throttled, &g.RetiredPages); err != nil {
			return err
		}
		g.Throttled = throttled == 1
	}
	return nil
}

// UUID turns a profile's readable device ID into the canonical hexadecimal
// UUID AMD GPU Agent expects from the vendor API, stably.
func UUID(id string) string {
	h := sha256.Sum256([]byte(id))
	h[6] = h[6]&0x0f | 0x80
	h[8] = h[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

// replace writes name in dir through a temporary file and a rename.
func replace(dir, name, text string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+name+"-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.WriteString(text)
	if err == nil {
		err = tmp.Chmod(0o644)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}
