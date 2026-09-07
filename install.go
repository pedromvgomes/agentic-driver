package agentic

import (
	"context"
	"fmt"
)

// Install fetches a version of the provider's CLI and refuses any bytes but the
// ones its pin names. An empty version means the provider's pin.
//
// It answers ErrInstallUnsupported for a provider that vendors no binary,
// rather than the provider having a method whose only job is to say so.
//
// Whether the bytes were also checked against a publisher's signature is a
// separate question, answered by SigningIdentity.
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

// SigningIdentity names the trust anchor this provider's binary was verified
// against.
//
// It answers ErrProvenanceUnsupported for a provider that pins a version
// without checking a publisher's signature over it. That is a real and
// different state from vendoring nothing at all, and a caller auditing what it
// runs needs to be able to tell the two apart: one runs bytes nobody vouched
// for at a version nobody chose, the other runs bytes whose digest is committed
// but whose builder this library did not confirm.
func (d *Driver) SigningIdentity() (string, error) {
	inst, ok := d.provider.(Installer)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrProvenanceUnsupported, d.descriptor.ID)
	}
	return inst.SigningIdentity(), nil
}

func (d *Driver) installer() (Pinner, error) {
	inst, ok := d.provider.(Pinner)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrInstallUnsupported, d.descriptor.ID)
	}
	return inst, nil
}
