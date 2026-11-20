// SPDX-License-Identifier: AGPL-3.0-only

// Package mockdriver gives the mock GPUs device nodes that can really be
// opened, one CUSE helper process per node. It does not emulate the amdgpu
// kernel driver: reads, writes and ioctls fail explicitly.
package mockdriver

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mesutoezdil/fake-gpu/pkg/profile"
	"golang.org/x/sys/unix"
)

const (
	kfdMinor   = 255 // /dev/kfd, registered first so the major is known
	drmMajor   = 226 // fixed DRM major: kind passes the host's nodes through
	kfdMajor   = 234 // the fixed major the nodes backend uses for /dev/kfd
	maxCards   = 64
	renderBase = 128
	markerFile = "device-major" // the dynamic major, read by libdrm and the next agent
)

// endpoint is one helper process and the nodes it backs.
type endpoint struct {
	cmd   *exec.Cmd
	done  chan struct{}
	minor uint32
	paths []string // below dev/, e.g. dri/renderD128
}

// Provider runs the helpers of one node. Errors reports a helper that died
// while it was meant to run.
type Provider struct {
	Errors chan error

	root, hostDev, helper, prefix string

	mu        sync.Mutex
	major     uint32 // dynamic major CUSE gave this provider
	previous  uint32 // major of the provider before a restart
	endpoints map[uint32]*endpoint
	binds     map[string]bool // host nodes bind-mounted over a DRM node
	closing   bool
}

// New checks the node can host the backend: CUSE is loaded, the helper is
// installed and no real amdgpu driver is present.
func New(root, hostDev, helper string) (*Provider, error) {
	if st, err := os.Stat("/dev/cuse"); err != nil || st.Mode()&os.ModeCharDevice == 0 {
		return nil, errors.New("CUSE backend requires host /dev/cuse; load the matching cuse kernel module")
	}
	if _, err := exec.LookPath(helper); err != nil {
		return nil, err
	}
	if realDriver() {
		return nil, errors.New("CUSE backend requires a CPU-only node without the real amdgpu module")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	host, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	// Helper names are global to the kernel; scope them to node and root.
	id := sha256.Sum256([]byte(host + ":" + abs))
	p := &Provider{
		Errors:    make(chan error, 1),
		root:      abs,
		hostDev:   hostDev,
		helper:    helper,
		prefix:    fmt.Sprintf("amd-mock-%x", id[:6]),
		endpoints: map[uint32]*endpoint{},
		binds:     map[string]bool{},
	}
	if data, err := os.ReadFile(filepath.Join(abs, markerFile)); err == nil {
		fmt.Sscan(string(data), &p.previous)
	}
	return p, nil
}

func realDriver() bool {
	_, err := os.Stat("/sys/module/amdgpu")
	return err == nil
}

// Reconcile registers /dev/kfd and a card and render node per device of p,
// fixed DPX partitions included, and drops nodes p no longer has. Majors are
// what the kernel hands out, never guessed or borrowed from a real GPU.
func (p *Provider) Reconcile(prof *profile.Profile) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	wanted := map[uint32][]string{kfdMinor: {"kfd"}}
	for _, d := range prof.Devices {
		if d.Index < 0 || d.Index >= maxCards || d.RenderMinor != renderBase+d.Index {
			return errors.New("CUSE requires contiguous DRM indices below 64")
		}
		wanted[uint32(d.Index)] = []string{fmt.Sprintf("dri/card%d", d.Index)}
		wanted[uint32(d.RenderMinor)] = []string{fmt.Sprintf("dri/renderD%d", d.RenderMinor)}
	}
	if err := p.ensure(kfdMinor, wanted[kfdMinor]); err != nil {
		return err
	}
	// Record the major before publishing more nodes: even a startup that
	// fails halfway can then reclaim its own stale nodes next time.
	if err := p.writeMarker(); err != nil {
		return err
	}
	for minor, paths := range wanted {
		if err := p.ensure(minor, paths); err != nil {
			return err
		}
	}
	for minor, e := range p.endpoints {
		if _, ok := wanted[minor]; !ok {
			p.stop(e)
		}
	}
	for _, e := range p.endpoints {
		if err := p.publish(e); err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) ensure(minor uint32, paths []string) error {
	if _, ok := p.endpoints[minor]; ok {
		return nil
	}
	return p.start(minor, paths)
}

func (p *Provider) writeMarker() error {
	path := filepath.Join(p.root, markerFile)
	if err := os.WriteFile(path+".tmp", []byte(fmt.Sprintln(p.major)), 0o644); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// start runs a helper and waits until CUSE has registered its node.
func (p *Provider) start(minor uint32, paths []string) error {
	name := fmt.Sprintf("%s-%d", p.prefix, minor)
	cmd := exec.Command(p.helper, name, strconv.Itoa(int(p.major)), strconv.Itoa(int(minor)))
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	e := &endpoint{cmd: cmd, done: make(chan struct{}), minor: minor, paths: paths}
	p.endpoints[minor] = e
	go p.watch(e, name)

	for range 100 {
		data, err := os.ReadFile(filepath.Join("/sys/class/cuse", name, "dev"))
		if err == nil {
			var major, got uint32
			if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d:%d", &major, &got); err != nil {
				return err
			}
			if got != minor || (p.major != 0 && p.major != major) {
				return fmt.Errorf("unexpected CUSE registration %s", data)
			}
			p.major = major
			return nil
		}
		select {
		case <-e.done:
			return fmt.Errorf("CUSE endpoint %s failed to register", name)
		case <-time.After(50 * time.Millisecond):
		}
	}
	return fmt.Errorf("CUSE registration timed out: %s", name)
}

// watch reports a helper that exits on its own.
func (p *Provider) watch(e *endpoint, name string) {
	err := e.cmd.Wait()
	close(e.done)
	p.mu.Lock()
	expected := p.closing || p.endpoints[e.minor] != e
	p.mu.Unlock()
	if !expected {
		select {
		case p.Errors <- fmt.Errorf("CUSE endpoint %s exited: %v", name, err):
		default:
		}
	}
}

// publish makes the endpoint's nodes exist under the root and on the host.
func (p *Provider) publish(e *endpoint) error {
	for _, rel := range e.paths {
		for _, dir := range []string{filepath.Join(p.root, "dev"), p.hostDev} {
			if err := p.publishNode(e, rel, dir); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *Provider) publishNode(e *endpoint, rel, dir string) error {
	path := filepath.Join(dir, rel)
	host := dir == p.hostDev
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var st unix.Stat_t
	err := unix.Lstat(path, &st)
	switch {
	case errors.Is(err, unix.ENOENT):
		return p.mknod(path, e.minor)
	case err != nil:
		return err
	}
	isChar := st.Mode&unix.S_IFMT == unix.S_IFCHR
	if isChar && uint64(st.Rdev) == unix.Mkdev(p.major, e.minor) {
		if host && p.previous == p.major && isMountpoint(path) {
			p.binds[path] = true // our own bind from before a restart
		}
		return nil
	}
	if host && isChar {
		old := unix.Major(uint64(st.Rdev))
		if old == p.previous && isMountpoint(path) {
			// A bind of the previous provider: take it down and look again.
			if err := unix.Unmount(path, unix.MNT_DETACH); err != nil {
				return err
			}
			delete(p.binds, path)
			if err := unix.Lstat(path, &st); err != nil {
				return err
			}
			old = unix.Major(uint64(st.Rdev))
		}
		if old == drmMajor {
			// A privileged kind node inherits the host's display devices.
			// Bind over them inside the node's mount namespace; never unlink.
			if err := unix.Mount(filepath.Join(p.root, "dev", rel), path, "", unix.MS_BIND, ""); err != nil {
				return fmt.Errorf("bind CUSE device %s: %w", path, err)
			}
			p.binds[path] = true
			return nil
		}
	}
	if isChar {
		// Replace only stale mock nodes: the nodes backend's fixed numbers or
		// one of this provider's majors. Never a device a driver registered.
		switch unix.Major(uint64(st.Rdev)) {
		case p.major, drmMajor, kfdMajor, p.previous:
		default:
			return fmt.Errorf("refusing unrelated device %s", path)
		}
		if _, err := os.Stat(fmt.Sprintf("/sys/dev/char/%d:%d", unix.Major(uint64(st.Rdev)), unix.Minor(uint64(st.Rdev)))); err == nil {
			return fmt.Errorf("refusing registered unrelated device %s", path)
		}
		if host && realDriver() {
			return errors.New("CUSE backend requires a CPU-only node; real amdgpu module present")
		}
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return p.mknod(path, e.minor)
}

func (p *Provider) mknod(path string, minor uint32) error {
	if err := unix.Mknod(path, unix.S_IFCHR|0o660, int(unix.Mkdev(p.major, minor))); err != nil {
		return fmt.Errorf("mknod %s: %w", path, err)
	}
	return os.Chmod(path, 0o660)
}

// owned reports whether path is one of this provider's nodes for minor.
func (p *Provider) owned(path string, minor uint32) bool {
	var st unix.Stat_t
	return unix.Lstat(path, &st) == nil && st.Mode&unix.S_IFMT == unix.S_IFCHR && uint64(st.Rdev) == unix.Mkdev(p.major, minor)
}

// cardEndpoints returns the card and render endpoints of GPU index.
func (p *Provider) cardEndpoints(index int) ([]*endpoint, error) {
	var out []*endpoint
	for _, minor := range []uint32{uint32(index), uint32(renderBase + index)} {
		e, ok := p.endpoints[minor]
		if !ok {
			return nil, fmt.Errorf("no CUSE endpoint for GPU %d", index)
		}
		out = append(out, e)
	}
	return out, nil
}

// RestoreDevice puts a recovered GPU's host nodes back. The endpoints are
// the same ones: a simulated fault never re-registers them.
func (p *Provider) RestoreDevice(index int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	eps, err := p.cardEndpoints(index)
	if err != nil {
		return err
	}
	for _, e := range eps {
		if err := p.publish(e); err != nil {
			return err
		}
	}
	return nil
}

// RemoveDevice takes a crashed GPU's host nodes away. The staged copies the
// CDI spec points at stay; libdrm refuses the GPU while its RAS state is fatal.
func (p *Provider) RemoveDevice(index int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	eps, err := p.cardEndpoints(index)
	if err != nil {
		return err
	}
	for _, e := range eps {
		for _, rel := range e.paths {
			path := filepath.Join(p.hostDev, rel)
			if p.binds[path] || !p.owned(path, e.minor) {
				continue // a bind stays; health shows in the RAS state
			}
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	return nil
}

// stop ends a helper and removes its nodes. It leaves the endpoint map first,
// so the watcher does not report the exit.
func (p *Provider) stop(e *endpoint) {
	delete(p.endpoints, e.minor)
	_ = e.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-e.done:
	case <-time.After(2 * time.Second):
		_ = e.cmd.Process.Kill()
		<-e.done
	}
	for _, rel := range e.paths {
		for _, dir := range []string{filepath.Join(p.root, "dev"), p.hostDev} {
			path := filepath.Join(dir, rel)
			switch {
			case !p.owned(path, e.minor):
			case p.binds[path]:
				_ = unix.Unmount(path, unix.MNT_DETACH)
				delete(p.binds, path)
			default:
				_ = os.Remove(path)
			}
		}
	}
}

// Close stops every helper and forgets the major.
func (p *Provider) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closing = true
	for _, e := range p.endpoints {
		p.stop(e)
	}
	_ = os.Remove(filepath.Join(p.root, markerFile))
}

// isMountpoint reports whether path is a mount target. Only this provider's
// own dynamic numbers qualify for stale-mount reclamation.
func isMountpoint(path string) bool {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\134`, `\`)
	for _, line := range strings.Split(string(data), "\n") {
		if f := strings.Fields(line); len(f) > 4 && unescape.Replace(f[4]) == path {
			return true
		}
	}
	return false
}
