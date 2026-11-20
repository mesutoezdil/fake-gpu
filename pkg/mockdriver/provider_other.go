// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux

package mockdriver

import (
	"errors"

	"github.com/mesutoezdil/fake-gpu/pkg/profile"
)

var errLinux = errors.New("CUSE requires Linux")

// Provider is unavailable outside Linux; New always fails.
type Provider struct{ Errors chan error }

func New(root, hostDev, helper string) (*Provider, error) { return nil, errLinux }

func (*Provider) Reconcile(*profile.Profile) error { return errLinux }
func (*Provider) RestoreDevice(int) error          { return errLinux }
func (*Provider) RemoveDevice(int) error           { return errLinux }
func (*Provider) Close()                           {}
