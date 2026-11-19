package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mesutoezdil/fake-gpu/pkg/gpu/kfd"
	"gopkg.in/yaml.v3"
)

func renderedRoot(t *testing.T, slug string) (string, *kfd.Profile) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "profiles", slug+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var profile kfd.Profile
	if err := yaml.Unmarshal(data, &profile); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := kfd.NewRenderer(&profile, root).RenderAll(); err != nil {
		t.Fatal(err)
	}
	return root, &profile
}

// The command captures a tree the renderer wrote and emits a profile the
// agent can load again.
func TestCapturesRenderedTreeToFile(t *testing.T) {
	root, want := renderedRoot(t, "mi300x")
	out := filepath.Join(t.TempDir(), "captured.yaml")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-root", root, "-o", out, "-name", "Test Card"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("-o must keep stdout empty, got %q", stdout.String())
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	header := "# Captured from 8 x Test Card\n"
	if !strings.HasPrefix(string(data), header) {
		t.Fatalf("output starts %q, want %q", strings.SplitN(string(data), "\n", 2)[0], header)
	}
	var got kfd.Profile
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatalf("captured profile does not parse: %v", err)
	}
	if len(got.Devices) != len(want.Devices) || got.DeviceDefault.Name != "Test Card" {
		t.Fatalf("captured %d devices named %q, want %d named Test Card", len(got.Devices), got.DeviceDefault.Name, len(want.Devices))
	}
}

func TestCapturesToStdout(t *testing.T) {
	root, _ := renderedRoot(t, "mi210")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-root", root}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "# Captured from ") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestFailures(t *testing.T) {
	rendered, _ := renderedRoot(t, "mi210")
	for _, tc := range []struct {
		name string
		args []string
		code int
		msg  string
	}{
		{"no GPUs under root", []string{"-root", t.TempDir()}, 1, "capture-profile:"},
		{"unknown flag", []string{"-bogus"}, 2, "flag provided but not defined"},
		{"stray argument", []string{"extra"}, 2, "unexpected arguments"},
		{"unwritable output", []string{"-root", rendered, "-o", "/nonexistent/dir/out.yaml"}, 1, "capture-profile:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(tc.args, &stdout, &stderr); code != tc.code {
				t.Fatalf("exit %d, want %d (stderr %q)", code, tc.code, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.msg) {
				t.Fatalf("stderr %q lacks %q", stderr.String(), tc.msg)
			}
		})
	}
}

func TestHelpExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-h"}, &stdout, &stderr); code != 0 {
		t.Fatalf("-h exit %d", code)
	}
	if !strings.Contains(stderr.String(), "-root") {
		t.Fatalf("usage lacks -root: %q", stderr.String())
	}
}

func TestFormatListsNotes(t *testing.T) {
	text, err := format(&kfd.Profile{Devices: make([]kfd.DeviceConfig, 2)}, []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"# Captured from 2 x \n", "# NOTE: first\n", "# NOTE: second\n"} {
		if !strings.Contains(text, line) {
			t.Errorf("format output lacks %q:\n%s", line, text)
		}
	}
}
