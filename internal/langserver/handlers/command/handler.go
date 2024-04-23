// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package command

import (
	"log"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/terraform-ls/internal/state"
	tfaddr "github.com/hashicorp/terraform-registry-address"
	tfmod "github.com/hashicorp/terraform-schema/module"
)

type FeatureReader interface {
	// ModulesFeature
	DeclaredModuleCalls(modPath string) (map[string]tfmod.DeclaredModuleCall, error)
	ProviderRequirements(modPath string) (tfmod.ProviderRequirements, error)
	CoreRequirements(modPath string) (version.Constraints, error)
	// RootModulesFeature
	CallersOfModule(modPath string) ([]string, error)
	InstalledModuleCalls(modPath string) (map[string]tfmod.InstalledModuleCall, error)
	InstalledProviders(modPath string) (map[tfaddr.Provider]*version.Version, error)
	TerraformVersion(modPath string) *version.Version
}

type CmdHandler struct {
	StateStore    *state.StateStore
	Logger        *log.Logger
	FeatureReader FeatureReader
}
