// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package variables

import (
	"context"
	"log"

	"github.com/hashicorp/terraform-ls/internal/document"
	"github.com/hashicorp/terraform-ls/internal/eventbus"
	"github.com/hashicorp/terraform-ls/internal/flavors/variables/jobs"
	"github.com/hashicorp/terraform-ls/internal/flavors/variables/state"
	"github.com/hashicorp/terraform-ls/internal/job"
	globalState "github.com/hashicorp/terraform-ls/internal/state"
	op "github.com/hashicorp/terraform-ls/internal/terraform/module/operation"
)

type VariablesFlavor struct {
	store    *state.VariableStore
	eventbus *eventbus.Nexus

	jobStore *globalState.JobStore
	fs       jobs.ReadOnlyFS
}

func NewVariablesFlavor(logger *log.Logger, eventbus *eventbus.Nexus, jobStore *globalState.JobStore, fs jobs.ReadOnlyFS) (*VariablesFlavor, error) {
	store, err := state.NewVariableStore(logger)
	if err != nil {
		return nil, err
	}

	return &VariablesFlavor{
		store:    store,
		eventbus: eventbus,
		jobStore: jobStore,
		fs:       fs,
	}, nil
}

func (f *VariablesFlavor) DidOpen(ctx context.Context, path string, languageID string) (job.IDs, error) {
	ids := make(job.IDs, 0)

	// Add to state if language ID matches
	if languageID == "terraform-vars" {
		err := f.store.AddIfNotExists(path)
		if err != nil {
			return ids, err
		}
	}

	// Schedule jobs if state entry exists
	hasVariableRecord := f.store.Exists(path)
	if !hasVariableRecord {
		return ids, nil
	}

	modHandle := document.DirHandleFromPath(path)
	parseVarsId, err := f.jobStore.EnqueueJob(ctx, job.Job{
		Dir: modHandle,
		Func: func(ctx context.Context) error {
			return jobs.ParseVariables(ctx, f.fs, f.store, path)
		},
		Type:        op.OpTypeParseVariables.String(),
		IgnoreState: true,
	})
	if err != nil {
		return ids, err
	}
	ids = append(ids, parseVarsId)

	varsRefsId, err := f.jobStore.EnqueueJob(ctx, job.Job{
		Dir: modHandle,
		Func: func(ctx context.Context) error {
			return jobs.DecodeVarsReferences(ctx, f.store, path)
		},
		Type:      op.OpTypeDecodeVarsReferences.String(),
		DependsOn: job.IDs{parseVarsId},
	})
	if err != nil {
		return ids, err
	}
	ids = append(ids, varsRefsId)

	return ids, nil
}
