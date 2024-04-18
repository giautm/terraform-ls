// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package decoder

import (
	"github.com/hashicorp/hcl-lang/decoder"
	"github.com/hashicorp/hcl-lang/reference"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform-ls/internal/flavors/variables/state"
	"github.com/hashicorp/terraform-ls/internal/terraform/ast"
	tfmod "github.com/hashicorp/terraform-schema/module"
	tfschema "github.com/hashicorp/terraform-schema/schema"
)

func variablePathContext(mod *state.VariableRecord, stateReader StateReader) (*decoder.PathContext, error) {
	variables := make(map[string]tfmod.Variable)
	// TODO: GET SCHEMA FROM MODULE
	// meta, err := stateReader.LocalModuleMeta(mod.Path())
	// if err == nil {
	// 	variables = meta.Variables
	// }

	schema, err := tfschema.SchemaForVariables(variables, mod.Path())
	if err != nil {
		return nil, err
	}

	pathCtx := &decoder.PathContext{
		Schema:           schema,
		ReferenceOrigins: make(reference.Origins, 0),
		ReferenceTargets: make(reference.Targets, 0),
		Files:            make(map[string]*hcl.File),
	}

	if len(schema.Attributes) > 0 {
		// Only validate if this is actually a module
		// as we may come across standalone tfvars files
		// for which we have no context.
		pathCtx.Validators = varsValidators
	}

	for _, origin := range mod.VarsRefOrigins {
		if ast.IsVarsFilename(origin.OriginRange().Filename) {
			pathCtx.ReferenceOrigins = append(pathCtx.ReferenceOrigins, origin)
		}
	}

	for name, f := range mod.ParsedVarsFiles {
		pathCtx.Files[name.String()] = f
	}

	return pathCtx, nil
}
