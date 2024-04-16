// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package stacks

import (
	"context"
	"log"

	"github.com/hashicorp/terraform-ls/internal/document"
	"github.com/hashicorp/terraform-ls/internal/flavors/stacks/jobs"
	"github.com/hashicorp/terraform-ls/internal/flavors/stacks/state"
	"github.com/hashicorp/terraform-ls/internal/job"
	globalState "github.com/hashicorp/terraform-ls/internal/state"
	op "github.com/hashicorp/terraform-ls/internal/terraform/module/operation"
)

type StacksFlavor struct {
	store *state.StacksStore

	jobStore *globalState.JobStore
	fs       jobs.ReadOnlyFS
}

func NewStacksFlavor(logger *log.Logger, jobStore *globalState.JobStore, fs jobs.ReadOnlyFS) (*StacksFlavor, error) {
	store, err := state.NewStacksStore(logger)
	if err != nil {
		return nil, err
	}

	return &StacksFlavor{
		store:    store,
		jobStore: jobStore,
		fs:       fs,
	}, nil
}

func (f *StacksFlavor) DidOpen(ctx context.Context, path string, languageID string) (job.IDs, error) {
	ids := make(job.IDs, 0)

	// Add to state if language ID matches
	if languageID == "terraform-stacks" {
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
			return jobs.ParseStack(ctx, f.fs, f.store, path)
		},
		Type:        op.OpTypeParseStacks.String(),
		IgnoreState: true,
	})
	if err != nil {
		return ids, err
	}
	ids = append(ids, parseVarsId)

	return ids, nil
}
