package agentic

import (
	"context"
	"fmt"
)

// Install fetches a version of the provider's CLI and verifies it against the
// publisher's signature. An empty version means the provider's pin.
//
// It answers ErrInstallUnsupported for a provider that vendors no binary,
// rather than the provider having a method whose only job is to say so.
func (d *Driver) Install(ctx context.Context, version string) (InstallResult, error) {
	inst, err := d.installer()
	if err != nil {
		return InstallResult{}, err
	}
	return inst.Install(ctx, version)
}

// Installed lists the versions present, newest first.
func (d *Driver) Installed(ctx context.Context) ([]string, error) {
	inst, err := d.installer()
	if err != nil {
		return nil, err
	}
	return inst.Installed(ctx)
}

// Prune trims old versions, keeping the newest keep of them and never removing
// the pin the driver would execute.
func (d *Driver) Prune(ctx context.Context, keep int) error {
	inst, err := d.installer()
	if err != nil {
		return err
	}
	return inst.Prune(ctx, keep)
}

func (d *Driver) installer() (Installer, error) {
	inst, ok := d.provider.(Installer)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrInstallUnsupported, d.descriptor.ID)
	}
	return inst, nil
}
