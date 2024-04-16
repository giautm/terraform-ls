// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package module

import (
	"context"
	"fmt"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/terraform-ls/internal/document"
	"github.com/hashicorp/terraform-ls/internal/job"
	"github.com/hashicorp/terraform-ls/internal/state"
	"github.com/hashicorp/terraform-ls/internal/terraform/datadir"
	op "github.com/hashicorp/terraform-ls/internal/terraform/module/operation"
	tfaddr "github.com/hashicorp/terraform-registry-address"
)

const tracerName = "github.com/hashicorp/terraform-ls/internal/terraform/module"

type DeferFunc func(opError error)

// GetTerraformVersion obtains "installed" Terraform version
// which can inform what version of core schema to pick.
// Knowing the version is not required though as we can rely on
// the constraint in `required_version` (as parsed via
// [LoadModuleMetadata] and compare it against known released versions.
func GetTerraformVersion(ctx context.Context, rootStore *state.RootStore, modPath string) error {
	mod, err := rootStore.RootRecordByPath(modPath)
	if err != nil {
		return err
	}

	// Avoid getting version if getting is already in progress or already known
	if mod.TerraformVersionState != op.OpStateUnknown && !job.IgnoreState(ctx) {
		return job.StateNotChangedErr{Dir: document.DirHandleFromPath(modPath)}
	}

	err = rootStore.SetTerraformVersionState(modPath, op.OpStateLoading)
	if err != nil {
		return err
	}
	defer rootStore.SetTerraformVersionState(modPath, op.OpStateLoaded)

	tfExec, err := TerraformExecutorForModule(ctx, mod.Path())
	if err != nil {
		sErr := rootStore.UpdateTerraformAndProviderVersions(modPath, nil, nil, err)
		if err != nil {
			return sErr
		}
		return err
	}

	v, pv, err := tfExec.Version(ctx)

	// TODO: Remove and rely purely on ParseProviderVersions
	// In most cases we get the provider version from the datadir/lockfile
	// but there is an edge case with custom plugin location
	// when this may not be available, so leveraging versions
	// from "terraform version" accounts for this.
	// See https://github.com/hashicorp/terraform-ls/issues/24
	pVersions := providerVersionsFromTfVersion(pv)

	sErr := rootStore.UpdateTerraformAndProviderVersions(modPath, v, pVersions, err)
	if sErr != nil {
		return sErr
	}

	return err
}

func providerVersionsFromTfVersion(pv map[string]*version.Version) map[tfaddr.Provider]*version.Version {
	m := make(map[tfaddr.Provider]*version.Version, 0)

	for rawAddr, v := range pv {
		pAddr, err := tfaddr.ParseProviderSource(rawAddr)
		if err != nil {
			// skip unparsable address
			continue
		}
		if pAddr.IsLegacy() {
			// TODO: check for migrations via Registry API?
		}
		m[pAddr] = v
	}

	return m
}

// ParseModuleManifest parses the "module manifest" which
// contains records of installed modules, e.g. where they're
// installed on the filesystem.
// This is useful for processing any modules which are not local
// nor hosted in the Registry (which would be handled by
// [GetModuleDataFromRegistry]).
func ParseModuleManifest(ctx context.Context, fs ReadOnlyFS, rootStore *state.RootStore, modPath string) error {
	mod, err := rootStore.RootRecordByPath(modPath)
	if err != nil {
		return err
	}

	// Avoid parsing if it is already in progress or already known
	if mod.ModManifestState != op.OpStateUnknown && !job.IgnoreState(ctx) {
		return job.StateNotChangedErr{Dir: document.DirHandleFromPath(modPath)}
	}

	err = rootStore.SetModManifestState(modPath, op.OpStateLoading)
	if err != nil {
		return err
	}

	manifestPath, ok := datadir.ModuleManifestFilePath(fs, modPath)
	if !ok {
		err := fmt.Errorf("%s: manifest file does not exist", modPath)
		sErr := rootStore.UpdateModManifest(modPath, nil, err)
		if sErr != nil {
			return sErr
		}
		return err
	}

	mm, err := datadir.ParseModuleManifestFromFile(manifestPath)
	if err != nil {
		err := fmt.Errorf("failed to parse manifest: %w", err)
		sErr := rootStore.UpdateModManifest(modPath, nil, err)
		if sErr != nil {
			return sErr
		}
		return err
	}

	sErr := rootStore.UpdateModManifest(modPath, mm, err)

	if sErr != nil {
		return sErr
	}
	return err
}

// ParseProviderVersions is a job complimentary to [ObtainSchema]
// in that it obtains versions of providers/schemas from Terraform
// CLI's lock file.
func ParseProviderVersions(ctx context.Context, fs ReadOnlyFS, rootStore *state.RootStore, modPath string) error {
	mod, err := rootStore.RootRecordByPath(modPath)
	if err != nil {
		return err
	}

	// Avoid parsing if it is already in progress or already known
	if mod.InstalledProvidersState != op.OpStateUnknown && !job.IgnoreState(ctx) {
		return job.StateNotChangedErr{Dir: document.DirHandleFromPath(modPath)}
	}

	err = rootStore.SetInstalledProvidersState(modPath, op.OpStateLoading)
	if err != nil {
		return err
	}

	pvm, err := datadir.ParsePluginVersions(fs, modPath)

	sErr := rootStore.UpdateInstalledProviders(modPath, pvm, err)
	if sErr != nil {
		return sErr
	}

	return err
}

// GetInstalledTerraformVersion obtains "installed" Terraform version
// which can inform what version of core schema to pick.
// It relies on Terraform CLI to run version subcommand and ignores all
// provider related information. We only aim to run this job once.
func GetInstalledTerraformVersion(ctx context.Context, tfVersionStore *state.TerraformVersionStore, path string) error {
	record, err := tfVersionStore.TerraformVersionRecord()
	if err != nil {
		return err
	}

	// Avoid getting version if getting is already in progress or already known
	if record.TerraformVersionState != op.OpStateUnknown && !job.IgnoreState(ctx) {
		return job.StateNotChangedErr{Dir: document.DirHandleFromPath(path)}
	}

	err = tfVersionStore.SetTerraformVersionState(op.OpStateLoading)
	if err != nil {
		return err
	}
	defer tfVersionStore.SetTerraformVersionState(op.OpStateLoaded)

	tfExec, err := TerraformExecutorForModule(ctx, path)
	if err != nil {
		return err
	}

	v, _, err := tfExec.Version(ctx)
	sErr := tfVersionStore.UpdateTerraformVersion(v, err)
	if sErr != nil {
		return sErr
	}

	return err
}
