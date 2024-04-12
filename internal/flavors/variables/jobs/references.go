// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package jobs

import (
	"context"

	"github.com/hashicorp/hcl-lang/decoder"
	"github.com/hashicorp/hcl-lang/lang"
	"github.com/hashicorp/terraform-ls/internal/document"
	"github.com/hashicorp/terraform-ls/internal/job"
	"github.com/hashicorp/terraform-ls/internal/state"
)

// DecodeVarsReferences collects reference origins within
// variable files (*.tfvars) where each valid attribute
// (as informed by schema provided via [LoadModuleMetadata])
// is considered an origin.
//
// This is useful in hovering over those variable names,
// go-to-definition and go-to-references.
func DecodeVarsReferences(ctx context.Context, varStore *state.VariableStore, stateReader state.StateReader, modPath string) error {
	mod, err := varStore.VariableRecordByPath(modPath)
	if err != nil {
		return err
	}

	// TODO: Avoid collection if upstream (parsing) job reported no changes

	// Avoid collection if it is already in progress or already done
	if mod.VarsRefOriginsState != op.OpStateUnknown && !job.IgnoreState(ctx) {
		return job.StateNotChangedErr{Dir: document.DirHandleFromPath(modPath)}
	}

	err = varStore.SetVarsReferenceOriginsState(modPath, op.OpStateLoading)
	if err != nil {
		return err
	}

	d := decoder.NewDecoder(&idecoder.PathReader{
		StateReader: stateReader,
	})
	d.SetContext(idecoder.DecoderContext(ctx))

	varsDecoder, err := d.Path(lang.Path{
		Path:       modPath,
		LanguageID: ilsp.Tfvars.String(),
	})
	if err != nil {
		return err
	}

	origins, rErr := varsDecoder.CollectReferenceOrigins()
	sErr := varStore.UpdateVarsReferenceOrigins(modPath, origins, rErr)
	if sErr != nil {
		return sErr
	}

	return rErr
}
