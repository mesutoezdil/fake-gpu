package mockdriver

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mesutoezdil/fake-gpu/pkg/profile"
	"golang.org/x/sys/unix"
)

// A dynamic major well above the fixed DRM (226) and KFD (234) numbers.
const testMajor = 511

func testProvider(t *testing.T) *Provider {
	t.Helper()
	return &Provider{
		binds:     map[string]bool{},
		root:      t.TempDir(),
		hostDev:   t.TempDir(),
		prefix:    "amd-mock-test",
		major:     testMajor,
		endpoints: map[uint32]*endpoint{},
		Errors:    make(chan error, 1),
	}
}

// fakeEndpoint stands in for a CUSE helper with a process that only waits.
func fakeEndpoint(t *testing.T, p *Provider, minor uint32, paths ...string) *endpoint {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	e := &endpoint{cmd: cmd, done: make(chan struct{}), minor: minor, paths: paths}
	go func() { _ = cmd.Wait(); close(e.done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-e.done })
	p.endpoints[minor] = e
	return e
}

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("mknod needs root")
	}
}

func charDevice(t *testing.T, path string) (uint32, uint32) {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		t.Fatal(err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFCHR {
		t.Fatalf("%s is not a character device", path)
	}
	return unix.Major(uint64(st.Rdev)), unix.Minor(uint64(st.Rdev))
}

func TestReconcileRejectsNonContiguousInventory(t *testing.T) {
	for _, dev := range []profile.Device{
		{Index: 0, RenderMinor: 130},
		{Index: -1, RenderMinor: 127},
		{Index: 64, RenderMinor: 192},
	} {
		p := testProvider(t)
		err := p.Reconcile(&profile.Profile{Devices: []profile.Device{dev}})
		if err == nil || !strings.Contains(err.Error(), "contiguous") {
			t.Errorf("%+v: Reconcile error = %v, want contiguous-index error", dev, err)
		}
		// Validation runs before any helper starts or any file is written.
		if len(p.endpoints) != 0 {
			t.Errorf("%+v: started %d endpoints before rejecting", dev, len(p.endpoints))
		}
		if _, err := os.Stat(filepath.Join(p.root, "device-major")); !os.IsNotExist(err) {
			t.Errorf("%+v: wrote device-major before rejecting", dev)
		}
	}
}

func TestMarkerIsWrittenAtomically(t *testing.T) {
	p := testProvider(t)
	if err := p.writeMarker(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(p.root, "device-major"))
	if err != nil || string(data) != "511\n" {
		t.Fatalf("device-major = %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(p.root, "device-major.tmp")); !os.IsNotExist(err) {
		t.Fatal("temporary file left behind")
	}
}

func TestUnknownGPUIsAnError(t *testing.T) {
	p := testProvider(t)
	if err := p.RemoveDevice(3); err == nil || !strings.Contains(err.Error(), "no CUSE endpoint for GPU 3") {
		t.Errorf("RemoveDevice error = %v", err)
	}
	if err := p.RestoreDevice(3); err == nil || !strings.Contains(err.Error(), "no CUSE endpoint for GPU 3") {
		t.Errorf("RestoreDevice error = %v", err)
	}
}

func TestIsMountpoint(t *testing.T) {
	if !isMountpoint("/") {
		t.Error("/ is not reported as a mount point")
	}
	if isMountpoint(filepath.Join(t.TempDir(), "plain")) {
		t.Error("a plain temporary path is reported as a mount point")
	}
}

func TestCloseStopsHelpersAndForgetsMajor(t *testing.T) {
	p := testProvider(t)
	e := fakeEndpoint(t, p, 255, "kfd")
	if err := p.writeMarker(); err != nil {
		t.Fatal(err)
	}
	p.Close()
	select {
	case <-e.done:
	default:
		t.Fatal("helper still running after Close")
	}
	if len(p.endpoints) != 0 {
		t.Fatalf("%d endpoints left after Close", len(p.endpoints))
	}
	if _, err := os.Stat(filepath.Join(p.root, "device-major")); !os.IsNotExist(err) {
		t.Fatal("device-major kept after Close")
	}
}

func TestPublishCreatesBothCopiesIdempotently(t *testing.T) {
	requireRoot(t)
	p := testProvider(t)
	e := fakeEndpoint(t, p, 128, "dri/renderD128")
	// A stale plain file where the node belongs is replaced.
	stale := filepath.Join(p.hostDev, "dri/renderD128")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := p.publish(e); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
		for _, base := range []string{filepath.Join(p.root, "dev"), p.hostDev} {
			if major, minor := charDevice(t, filepath.Join(base, "dri/renderD128")); major != testMajor || minor != 128 {
				t.Fatalf("pass %d: %s node is %d:%d, want %d:128", i, base, major, minor, testMajor)
			}
		}
	}
}

func TestPublishRefusesUnrelatedDevice(t *testing.T) {
	requireRoot(t)
	p := testProvider(t)
	e := fakeEndpoint(t, p, 128, "dri/renderD128")
	// 1:3 is /dev/null: a registered device that belongs to someone else.
	path := filepath.Join(p.hostDev, "dri/renderD128")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mknod(path, unix.S_IFCHR|0o600, int(unix.Mkdev(1, 3))); err != nil {
		t.Fatal(err)
	}
	if err := p.publish(e); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("nodes error = %v, want a refusal", err)
	}
	if major, minor := charDevice(t, path); major != 1 || minor != 3 {
		t.Fatalf("unrelated device replaced with %d:%d", major, minor)
	}
}

func TestRemoveDeviceKeepsStagedCopy(t *testing.T) {
	requireRoot(t)
	p := testProvider(t)
	card := fakeEndpoint(t, p, 0, "dri/card0")
	render := fakeEndpoint(t, p, 128, "dri/renderD128")
	for _, e := range []*endpoint{card, render} {
		if err := p.publish(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.RemoveDevice(0); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"dri/card0", "dri/renderD128"} {
		if _, err := os.Lstat(filepath.Join(p.hostDev, rel)); !os.IsNotExist(err) {
			t.Errorf("host %s survived a simulated crash", rel)
		}
		charDevice(t, filepath.Join(p.root, "dev", rel))
	}
	if err := p.RestoreDevice(0); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"dri/card0", "dri/renderD128"} {
		charDevice(t, filepath.Join(p.hostDev, rel))
	}
}
