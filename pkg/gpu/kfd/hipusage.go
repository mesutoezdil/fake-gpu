package kfd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ReadHIPUsage returns the device memory that live libfakehip clients hold,
// in bytes by GPU index. Every client keeps its usage file locked for as long
// as it runs, so a file that can be locked here belongs to a process that
// exited without cleaning up; it is removed and not counted.
func ReadHIPUsage(root string) map[int]uint64 {
	usage := map[int]uint64{}
	dir := filepath.Join(root, "hip")
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			continue
		}
		if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
			os.Remove(path)
			f.Close()
			continue
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			var gpu int
			var bytes uint64
			if n, _ := fmt.Sscan(scanner.Text(), &gpu, &bytes); n == 2 && gpu >= 0 {
				usage[gpu] += bytes
			}
		}
		f.Close()
	}
	return usage
}
