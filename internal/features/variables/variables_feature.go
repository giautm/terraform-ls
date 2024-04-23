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
	fdecoder "github.com/hashicorp/terraform-ls/internal/features/variables/decoder"
	"github.com/hashicorp/terraform-ls/internal/features/variables/jobs"
	"github.com/hashicorp/terraform-ls/internal/features/variables/state"
	"github.com/hashicorp/terraform-ls/internal/job"
	globalState "github.com/hashicorp/terraform-ls/internal/state"
	op "github.com/hashicorp/terraform-ls/internal/terraform/module/operation"
)

type VariablesFeature struct {
	store    *state.VariableStore
	eventbus *eventbus.EventBus
	stopFunc context.CancelFunc
	logger   *log.Logger

	moduleFeature fdecoder.ModuleReader
	jobStore      *globalState.JobStore
	fs            jobs.ReadOnlyFS
}

func NewVariablesFeature(eventbus *eventbus.EventBus, jobStore *globalState.JobStore, fs jobs.ReadOnlyFS, moduleFeature fdecoder.ModuleReader) (*VariablesFeature, error) {
	store, err := state.NewVariableStore()
	if err != nil {
		return nil, err
	}
	discardLogger := log.New(io.Discard, "", 0)

	return &VariablesFeature{
		store:         store,
		eventbus:      eventbus,
		stopFunc:      func() {},
		logger:        discardLogger,
		moduleFeature: moduleFeature,
		jobStore:      jobStore,
		fs:            fs,
	}, nil
}

func (f *VariablesFeature) SetLogger(logger *log.Logger) {
	f.logger = logger
	f.store.SetLogger(logger)
}

func (f *VariablesFeature) Start(ctx context.Context) {
	ctx, cancelFunc := context.WithCancel(ctx)
	f.stopFunc = cancelFunc

	didOpen := f.eventbus.OnDidOpen("feature.variables")
	didChange := f.eventbus.OnDidChange("feature.variables")
	go func() {
		for {
			select {
			case open := <-didOpen:
				f.DidOpen(open.Context, open.Dir, open.LanguageID)
			case didChange := <-didChange:
				// TODO move into own handler
				f.DidOpen(didChange.Context, didChange.Dir, didChange.LanguageID)

			case <-ctx.Done():
				return
			}
		}
	}()
}

func (f *VariablesFeature) Stop() {
	f.stopFunc()
	f.logger.Print("stopped modules feature")
}

func (f *VariablesFeature) DidOpen(ctx context.Context, dir document.DirHandle, languageID string) (job.IDs, error) {
	ids := make(job.IDs, 0)
	path := dir.Path()

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

	parseVarsId, err := f.jobStore.EnqueueJob(ctx, job.Job{
		Dir: dir,
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
		Dir: dir,
		Func: func(ctx context.Context) error {
			return jobs.DecodeVarsReferences(ctx, f.store, f.moduleFeature, path)
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

func (f *VariablesFeature) PathContext(path lang.Path) (*decoder.PathContext, error) {
	pathReader := &fdecoder.PathReader{
		StateReader:  f.store,
		ModuleReader: f.moduleFeature,
	}

	return pathReader.PathContext(path)
}

func (f *VariablesFeature) Paths(ctx context.Context) []lang.Path {
	paths := make([]lang.Path, 0)

	// TODO

	return paths
}
