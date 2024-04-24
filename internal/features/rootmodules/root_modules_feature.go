// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package rootmodules

import (
	"context"
	"io"
	"log"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/terraform-ls/internal/eventbus"
	"github.com/hashicorp/terraform-ls/internal/features/rootmodules/jobs"
	"github.com/hashicorp/terraform-ls/internal/features/rootmodules/state"
	globalState "github.com/hashicorp/terraform-ls/internal/state"
	"github.com/hashicorp/terraform-ls/internal/terraform/exec"
	tfaddr "github.com/hashicorp/terraform-registry-address"
	tfmod "github.com/hashicorp/terraform-schema/module"
)

type RootModulesFeature struct {
	store    *state.RootStore
	eventbus *eventbus.EventBus
	stopFunc context.CancelFunc
	logger   *log.Logger

	tfExecFactory exec.ExecutorFactory
	jobStore      *globalState.JobStore
	fs            jobs.ReadOnlyFS
}

func NewRootModulesFeature(eventbus *eventbus.EventBus, tfExecFactory exec.ExecutorFactory, jobStore *globalState.JobStore, fs jobs.ReadOnlyFS) (*RootModulesFeature, error) {
	store, err := state.NewRootStore()
	if err != nil {
		return nil, err
	}
	discardLogger := log.New(io.Discard, "", 0)

	return &RootModulesFeature{
		store:         store,
		eventbus:      eventbus,
		stopFunc:      func() {},
		logger:        discardLogger,
		tfExecFactory: tfExecFactory,
		jobStore:      jobStore,
		fs:            fs,
	}, nil
}

func (f *RootModulesFeature) SetLogger(logger *log.Logger) {
	f.logger = logger
	f.store.SetLogger(logger)
}

func (f *RootModulesFeature) Start(ctx context.Context) {
	ctx, cancelFunc := context.WithCancel(ctx)
	f.stopFunc = cancelFunc

	didOpen := f.eventbus.OnDidOpen("feature.rootmodules")
	discover := f.eventbus.OnDiscover("feature.rootmodules")
	go func() {
		for {
			select {
			case open := <-didOpen:
				f.didOpen(open.Context, open.Dir, open.LanguageID)
			case discover := <-discover:
				// TODO collect errors
				f.discover(discover.Path, discover.Files)

			case <-ctx.Done():
				return
			}
		}
	}()
}

func (f *RootModulesFeature) Stop() {
	f.stopFunc()
	f.logger.Print("stopped root modules feature")
}

func (f *RootModulesFeature) InstalledModuleCalls(modPath string) (map[string]tfmod.InstalledModuleCall, error) {
	return f.store.InstalledModuleCalls(modPath)
}

func (f *RootModulesFeature) TerraformVersion(modPath string) *version.Version {
	version, err := f.store.RootRecordByPath(modPath)
	if err != nil {
		return nil
	}

	return version.TerraformVersion
}

func (f *RootModulesFeature) InstalledProviders(modPath string) (map[tfaddr.Provider]*version.Version, error) {
	record, err := f.store.RootRecordByPath(modPath)
	if err != nil {
		return nil, err
	}

	return record.InstalledProviders, nil
}

func (f *RootModulesFeature) CallersOfModule(modPath string) ([]string, error) {
	return f.store.CallersOfModule(modPath)
}
