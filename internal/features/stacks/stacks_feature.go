// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package stacks

import (
	"context"
	"log"

	"github.com/hashicorp/hcl-lang/decoder"
	"github.com/hashicorp/hcl-lang/lang"
	"github.com/hashicorp/terraform-ls/internal/document"
	"github.com/hashicorp/terraform-ls/internal/eventbus"
	fdecoder "github.com/hashicorp/terraform-ls/internal/features/stacks/decoder"
	"github.com/hashicorp/terraform-ls/internal/features/stacks/jobs"
	"github.com/hashicorp/terraform-ls/internal/features/stacks/state"
	"github.com/hashicorp/terraform-ls/internal/job"
	globalState "github.com/hashicorp/terraform-ls/internal/state"
	op "github.com/hashicorp/terraform-ls/internal/terraform/module/operation"
)

type StacksFeature struct {
	store    *state.StacksStore
	eventbus *eventbus.EventBus
	stopFunc context.CancelFunc
	logger   *log.Logger

	jobStore *globalState.JobStore
	fs       jobs.ReadOnlyFS
}

func NewStacksFeature(logger *log.Logger, eventbus *eventbus.EventBus, jobStore *globalState.JobStore, fs jobs.ReadOnlyFS) (*StacksFeature, error) {
	store, err := state.NewStacksStore(logger)
	if err != nil {
		return nil, err
	}

	return &StacksFeature{
		store:    store,
		eventbus: eventbus,
		stopFunc: func() {},
		logger:   logger,
		jobStore: jobStore,
		fs:       fs,
	}, nil
}

func (f *StacksFeature) Start(ctx context.Context) {
	ctx, cancelFunc := context.WithCancel(ctx)
	f.stopFunc = cancelFunc

	didOpen := f.eventbus.OnDidOpen("feature.stacks")
	didChange := f.eventbus.OnDidChange("feature.stacks")
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

func (f *StacksFeature) Stop() {
	f.stopFunc()
	f.logger.Print("stopped modules feature")
}

func (f *StacksFeature) DidOpen(ctx context.Context, dir document.DirHandle, languageID string) (job.IDs, error) {
	ids := make(job.IDs, 0)
	path := dir.Path()

	if languageID != "terraform-stacks" {
		// we should return here if the languageID is not "terraform-stacks"
		// so we don't attempt to process a language that's not stacks
		return ids, nil
	}

	// Add to state if it doesnt exist
	err := f.store.AddIfNotExists(path)
	if err != nil {
		return ids, err
	}

	// Schedule jobs if state entry exists
	hasStacksRecord := f.store.Exists(path)
	if !hasStacksRecord {
		return ids, nil
	}

	parseStacksId, err := f.jobStore.EnqueueJob(ctx, job.Job{
		Dir: dir,
		Func: func(ctx context.Context) error {
			return jobs.ParseStack(ctx, f.fs, f.store, path)
		},
		Type:        op.OpTypeParseStacks.String(),
		IgnoreState: true,
	})
	if err != nil {
		return ids, err
	}
	ids = append(ids, parseStacksId)

	return ids, nil
}

func (f *StacksFeature) PathContext(path lang.Path) (*decoder.PathContext, error) {
	pathReader := &fdecoder.PathReader{
		StateReader: f.store,
	}

	return pathReader.PathContext(path)
}

func (f *StacksFeature) Paths(ctx context.Context) []lang.Path {
	paths := make([]lang.Path, 0)

	// TODO

	return paths
}
