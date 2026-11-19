package mockdriver

import "testing"

// Without CUSE, its helper, or Linux at all, New must refuse rather than
// hand back a provider that fails later.
func TestNewRefusesWithoutPrerequisites(t *testing.T) {
	if p, err := New(t.TempDir(), t.TempDir(), "fake-gpu-no-such-helper"); err == nil {
		p.Close()
		t.Fatal("New succeeded without a usable CUSE helper")
	}
}
