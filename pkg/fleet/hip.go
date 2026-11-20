package fleet

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ReadHIPUsage returns the device memory live libfakehip clients hold, in
// bytes by GPU index. A client keeps its usage file locked while it runs, so
// a file that can be locked here is left over by a process that died; it is
// removed and not counted.
func ReadHIPUsage(root string) map[int]uint64 {
	usage := map[int]uint64{}
	dir := filepath.Join(root, "hip")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		readClient(filepath.Join(dir, e.Name()), usage)
	}
	return usage
}

func readClient(path string, usage map[int]uint64) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return
	}
	defer f.Close()
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
		os.Remove(path)
		return
	}
	lines := bufio.NewScanner(f)
	for lines.Scan() {
		var gpu int
		var bytes uint64
		if n, _ := fmt.Sscan(lines.Text(), &gpu, &bytes); n == 2 && gpu >= 0 {
			usage[gpu] += bytes
		}
	}
}
