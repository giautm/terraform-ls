// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package variables

import (
	"context"
	"io"
	"log"

	"github.com/hashicorp/hcl-lang/decoder"
	"github.com/hashicorp/hcl-lang/lang"
	"github.com/hashicorp/terraform-ls/internal/eventbus"
	fdecoder "github.com/hashicorp/terraform-ls/internal/features/variables/decoder"
	"github.com/hashicorp/terraform-ls/internal/features/variables/jobs"
	"github.com/hashicorp/terraform-ls/internal/features/variables/state"
	"github.com/hashicorp/terraform-ls/internal/langserver/diagnostics"
	globalState "github.com/hashicorp/terraform-ls/internal/state"
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

func NewVariablesFeature(eventbus *eventbus.EventBus, jobStore *globalState.JobStore, changeStore *globalState.ChangeStore, fs jobs.ReadOnlyFS, moduleFeature fdecoder.ModuleReader) (*VariablesFeature, error) {
	store, err := state.NewVariableStore(changeStore)
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
	discover := f.eventbus.OnDiscover("feature.variables")
	go func() {
		for {
			select {
			case open := <-didOpen:
				f.didOpen(open.Context, open.Dir, open.LanguageID)
			case didChange := <-didChange:
				// TODO move into own handler
				f.didOpen(didChange.Context, didChange.Dir, didChange.LanguageID)
			case discover := <-discover:
				// TODO collect errors
				f.discover(discover.Path, discover.Files)

			case <-ctx.Done():
				return
			}
		}
	}()
}

func (f *VariablesFeature) Stop() {
	f.stopFunc()
	f.logger.Print("stopped variables feature")
}

func (f *VariablesFeature) PathContext(path lang.Path) (*decoder.PathContext, error) {
	pathReader := &fdecoder.PathReader{
		StateReader:  f.store,
		ModuleReader: f.moduleFeature,
	}

	return pathReader.PathContext(path)
}

func (f *VariablesFeature) Paths(ctx context.Context) []lang.Path {
	pathReader := &fdecoder.PathReader{
		StateReader:  f.store,
		ModuleReader: f.moduleFeature,
	}

	return pathReader.Paths(ctx)
}

func (f *VariablesFeature) Diagnostics(path string) diagnostics.Diagnostics {
	diags := diagnostics.NewDiagnostics()

	mod, err := f.store.VariableRecordByPath(path)
	if err != nil {
		return diags
	}

	for source, dm := range mod.VarsDiagnostics {
		diags.Append(source, dm.AutoloadedOnly().AsMap())
	}

	return diags
}
