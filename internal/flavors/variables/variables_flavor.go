// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package variables

import (
	"context"
	"io"
	"log"

	"github.com/hashicorp/hcl-lang/decoder"
	"github.com/hashicorp/hcl-lang/lang"
	"github.com/hashicorp/terraform-ls/internal/document"
	"github.com/hashicorp/terraform-ls/internal/eventbus"
	fdecoder "github.com/hashicorp/terraform-ls/internal/flavors/variables/decoder"
	"github.com/hashicorp/terraform-ls/internal/flavors/variables/jobs"
	"github.com/hashicorp/terraform-ls/internal/flavors/variables/state"
	"github.com/hashicorp/terraform-ls/internal/job"
	globalState "github.com/hashicorp/terraform-ls/internal/state"
	op "github.com/hashicorp/terraform-ls/internal/terraform/module/operation"
)

type VariablesFlavor struct {
	store    *state.VariableStore
	eventbus *eventbus.EventBus
	stopFunc context.CancelFunc
	logger   *log.Logger

	jobStore *globalState.JobStore
	fs       jobs.ReadOnlyFS
}

func NewVariablesFlavor(eventbus *eventbus.EventBus, jobStore *globalState.JobStore, fs jobs.ReadOnlyFS) (*VariablesFlavor, error) {
	store, err := state.NewVariableStore()
	if err != nil {
		return nil, err
	}
	discardLogger := log.New(io.Discard, "", 0)

	return &VariablesFlavor{
		store:    store,
		eventbus: eventbus,
		stopFunc: func() {},
		logger:   discardLogger,
		jobStore: jobStore,
		fs:       fs,
	}, nil
}

func (f *VariablesFlavor) SetLogger(logger *log.Logger) {
	f.logger = logger
	f.store.SetLogger(logger)
}

func (f *VariablesFlavor) Run(ctx context.Context) {
	ctx, cancelFunc := context.WithCancel(ctx)
	f.stopFunc = cancelFunc

	didOpen := f.eventbus.OnDidOpen("flavor.variables")
	didChange := f.eventbus.OnDidChange("flavor.variables")
	go func() {
		for {
			select {
			case open := <-didOpen:
				f.DidOpen(open.Context, open.Path, open.LanguageID)
			case didChange := <-didChange:
				// TODO move into own handler
				f.DidOpen(didChange.Context, didChange.Path, didChange.LanguageID)

			case <-ctx.Done():
				return
			}
		}
	}()
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

func (f *VariablesFlavor) PathContext(path lang.Path) (*decoder.PathContext, error) {
	pathReader := &fdecoder.PathReader{
		StateReader: f.store,
	}

	return pathReader.PathContext(path)
}

func (f *VariablesFlavor) Paths(ctx context.Context) []lang.Path {
	paths := make([]lang.Path, 0)

	// TODO

	return paths
}
