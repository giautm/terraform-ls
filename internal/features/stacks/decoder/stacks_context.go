// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package decoder

import (
	"github.com/hashicorp/hcl-lang/decoder"
	"github.com/hashicorp/hcl-lang/reference"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform-ls/internal/features/stacks/state"
)

func stacksPathContext(mod *state.StacksRecord, stateReader StateReader) (*decoder.PathContext, error) {
	schema, err := schemaForStack(mod, stateReader)
	if err != nil {
		return nil, err
	}

	pathCtx := &decoder.PathContext{
		Schema:           schema,
		ReferenceOrigins: make(reference.Origins, 0),
		ReferenceTargets: make(reference.Targets, 0),
		Files:            make(map[string]*hcl.File, 0),
	}

	for name, f := range mod.ParsedStacksFiles {
		pathCtx.Files[name.String()] = f
	}

	return pathCtx, nil
}
