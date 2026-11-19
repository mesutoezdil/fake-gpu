package main

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/containerd/nri/pkg/api"
)

func testPlugin() *plugin {
	return newPlugin("/var/lib/fake-gpu", "/opt/fake-gpu", excludedNamespaces("kube-system", "amd-mock"))
}

func gpuContainer() *api.Container {
	return &api.Container{Name: "app", Linux: &api.LinuxContainer{Devices: []*api.LinuxDevice{{Path: "/dev/dri/renderD128"}}}}
}

func TestExcludedNamespaces(t *testing.T) {
	got := excludedNamespaces(" kube-system, ,monitoring ,", "amd-mock")
	want := map[string]bool{"amd-mock": true, "kube-system": true, "monitoring": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("excludedNamespaces = %v, want %v", got, want)
	}
	if got := excludedNamespaces("", "amd-mock"); !reflect.DeepEqual(got, map[string]bool{"amd-mock": true}) {
		t.Fatalf("empty list must still exclude the release namespace, got %v", got)
	}
}

func TestCreateContainerSkips(t *testing.T) {
	p := testPlugin()
	for _, tc := range []struct {
		name string
		pod  *api.PodSandbox
		ctr  *api.Container
	}{
		{"release namespace", &api.PodSandbox{Namespace: "amd-mock"}, gpuContainer()},
		{"excluded namespace", &api.PodSandbox{Namespace: "kube-system"}, gpuContainer()},
		{"opt-out annotation", &api.PodSandbox{Namespace: "default", Annotations: map[string]string{"fake-gpu.amd.com/inject": "false"}}, gpuContainer()},
		{"no GPU and no annotation", &api.PodSandbox{Namespace: "default"}, &api.Container{Name: "app"}},
		{"unrelated device", &api.PodSandbox{Namespace: "default"}, &api.Container{Name: "app", Linux: &api.LinuxContainer{Devices: []*api.LinuxDevice{{Path: "/dev/null"}}}}},
		{"device annotation not true", &api.PodSandbox{Namespace: "default", Annotations: map[string]string{"fake-gpu.amd.com/devices": "yes"}}, &api.Container{Name: "app"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adj, updates, err := p.CreateContainer(context.Background(), tc.pod, tc.ctr)
			if err != nil || adj != nil || updates != nil {
				t.Fatalf("CreateContainer = %v, %v, %v; want no adjustment", adj, updates, err)
			}
		})
	}
}

func mounts(t *testing.T, adj *api.ContainerAdjustment) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, mount := range adj.Mounts {
		if !reflect.DeepEqual(mount.Options, []string{"rbind", "ro"}) || mount.Type != "bind" {
			t.Fatalf("mount %v is not a read-only bind", mount)
		}
		m[mount.Destination] = mount.Source
	}
	return m
}

func env(adj *api.ContainerAdjustment) map[string]string {
	e := map[string]string{}
	for _, kv := range adj.Env {
		e[kv.Key] = kv.Value
	}
	return e
}

func TestCreateContainerInjectsOverlayForAllocatedGPU(t *testing.T) {
	// An opt-out annotation other than "false" does not opt out.
	pod := &api.PodSandbox{Namespace: "default", Name: "p", Annotations: map[string]string{"fake-gpu.amd.com/inject": "true"}}
	for _, path := range []string{"/dev/kfd", "/dev/dri/renderD129"} {
		ctr := &api.Container{Name: "app", Linux: &api.LinuxContainer{Devices: []*api.LinuxDevice{{Path: path}}}}
		adj, _, err := testPlugin().CreateContainer(context.Background(), pod, ctr)
		if err != nil || adj == nil {
			t.Fatalf("%s: CreateContainer = %v, %v; want an adjustment", path, adj, err)
		}
		wantMounts := map[string]string{
			"/opt/fake-gpu":      "/var/lib/fake-gpu",
			"/sys/class/kfd":     "/var/lib/fake-gpu/sys/class/kfd",
			"/sys/module/amdgpu": "/var/lib/fake-gpu/sys/module/amdgpu",
		}
		if got := mounts(t, adj); !reflect.DeepEqual(got, wantMounts) {
			t.Errorf("%s: mounts = %v, want %v", path, got, wantMounts)
		}
		wantEnv := map[string]string{
			"LD_LIBRARY_PATH":       "/usr/lib64:/opt/fake-gpu/driver/usr/lib64",
			"MOCK_AMDSMI_STATE_DIR": "/opt/fake-gpu/smi",
			"ROCR_VISIBLE_DEVICES":  "all",
			"FAKE_GPU":              "1",
		}
		if got := env(adj); !reflect.DeepEqual(got, wantEnv) {
			t.Errorf("%s: env = %v, want %v", path, got, wantEnv)
		}
		// The allocator already gave the container its devices.
		if adj.Linux != nil && len(adj.Linux.Devices) != 0 {
			t.Errorf("%s: injected devices %v into an allocated container", path, adj.Linux.Devices)
		}
	}
}

func TestCreateContainerInjectsDevicesForAnnotation(t *testing.T) {
	pod := &api.PodSandbox{Namespace: "default", Annotations: map[string]string{"fake-gpu.amd.com/devices": "true"}}
	adj, _, err := testPlugin().CreateContainer(context.Background(), pod, &api.Container{Name: "app"})
	if err != nil || adj == nil || adj.Linux == nil {
		t.Fatalf("CreateContainer = %v, %v; want devices", adj, err)
	}
	devices := adj.Linux.Devices
	if len(devices) != 9 {
		t.Fatalf("got %d devices, want /dev/kfd and 8 render nodes", len(devices))
	}
	if d := devices[0]; d.Path != "/dev/kfd" || d.Type != "c" || d.Major != 234 || d.Minor != 0 || d.FileMode.GetValue() != 0o660 {
		t.Errorf("kfd device = %v", d)
	}
	for i, d := range devices[1:] {
		want := fmt.Sprintf("/dev/dri/renderD%d", 128+i)
		if d.Path != want || d.Type != "c" || d.Major != 226 || d.Minor != int64(128+i) || d.FileMode.GetValue() != 0o660 {
			t.Errorf("device %d = %v, want %s 226:%d mode 0660", i, d, want, 128+i)
		}
	}
	if len(mounts(t, adj)) != 3 {
		t.Errorf("annotated container must also get the overlay mounts")
	}
}

func TestContainerHasGPU(t *testing.T) {
	for _, tc := range []struct {
		ctr  *api.Container
		want bool
	}{
		{&api.Container{}, false},
		{&api.Container{Linux: &api.LinuxContainer{}}, false},
		{&api.Container{Linux: &api.LinuxContainer{Devices: []*api.LinuxDevice{{Path: "/dev/dri/card0"}}}}, false},
		{&api.Container{Linux: &api.LinuxContainer{Devices: []*api.LinuxDevice{{Path: "/dev/kfd"}}}}, true},
		{&api.Container{Linux: &api.LinuxContainer{Devices: []*api.LinuxDevice{{Path: "/dev/null"}, {Path: "/dev/dri/renderD130"}}}}, true},
	} {
		if got := containerHasGPU(tc.ctr); got != tc.want {
			t.Errorf("containerHasGPU(%v) = %v, want %v", tc.ctr.GetLinux().GetDevices(), got, tc.want)
		}
	}
}
