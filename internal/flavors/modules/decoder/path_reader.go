// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package decoder

import (
	"context"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hcl-lang/decoder"
	"github.com/hashicorp/hcl-lang/lang"
	"github.com/hashicorp/terraform-ls/internal/flavors/modules/state"
	ilsp "github.com/hashicorp/terraform-ls/internal/lsp"
	tfaddr "github.com/hashicorp/terraform-registry-address"
	tfmod "github.com/hashicorp/terraform-schema/module"
	"github.com/hashicorp/terraform-schema/registry"
	tfschema "github.com/hashicorp/terraform-schema/schema"
)

type StateReader interface {
	DeclaredModuleCalls(modPath string) (map[string]tfmod.DeclaredModuleCall, error)
	LocalModuleMeta(modPath string) (*tfmod.Meta, error)
	ModuleRecordByPath(modPath string) (*state.ModuleRecord, error)
	List() ([]*state.ModuleRecord, error)

	InstalledModuleCalls(modPath string) (map[string]tfmod.InstalledModuleCall, error)
	RegistryModuleMeta(addr tfaddr.Module, cons version.Constraints) (*registry.ModuleData, error)
	ProviderSchema(modPath string, addr tfaddr.Provider, vc version.Constraints) (*tfschema.ProviderSchema, error)
	InstalledTerraformVersion(modPath string) *version.Version
}

type PathReader struct {
	StateReader StateReader
}

var _ decoder.PathReader = &PathReader{}

func (pr *PathReader) Paths(ctx context.Context) []lang.Path {
	paths := make([]lang.Path, 0)

	moduleRecords, err := pr.StateReader.List()
	if err == nil {
		for _, record := range moduleRecords {
			paths = append(paths, lang.Path{
				Path:       record.Path(),
				LanguageID: ilsp.Terraform.String(),
			})
		}
	}

	return paths
}

// PathContext returns a PathContext for the given path based on the language ID.
func (pr *PathReader) PathContext(path lang.Path) (*decoder.PathContext, error) {
	mod, err := pr.StateReader.ModuleRecordByPath(path.Path)
	if err != nil {
		return nil, err
	}
	return modulePathContext(mod, pr.StateReader)
}
