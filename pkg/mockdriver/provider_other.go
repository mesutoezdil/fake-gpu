//go:build !linux

package mockdriver

import (
	"fmt"
	"github.com/mesutoezdil/fake-gpu/pkg/profile"
)

type Provider struct{ Errors chan error }

func New(root, hostDev, helper string) (*Provider, error) {
	return nil, fmt.Errorf("CUSE requires Linux")
}
func (p *Provider) Reconcile(*profile.Profile) error { return fmt.Errorf("CUSE requires Linux") }
func (p *Provider) RestoreDevice(index int) error    { return fmt.Errorf("CUSE requires Linux") }
func (p *Provider) Close()                           {}

func (p *Provider) RemoveDevice(index int) error { return fmt.Errorf("CUSE requires Linux") }
