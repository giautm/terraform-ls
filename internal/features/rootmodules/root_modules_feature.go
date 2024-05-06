// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package rootmodules

import (
	"context"
	"fmt"
	"io"
	"log"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/terraform-ls/internal/eventbus"
	"github.com/hashicorp/terraform-ls/internal/features/rootmodules/jobs"
	"github.com/hashicorp/terraform-ls/internal/features/rootmodules/state"
	globalState "github.com/hashicorp/terraform-ls/internal/state"
	"github.com/hashicorp/terraform-ls/internal/telemetry"
	"github.com/hashicorp/terraform-ls/internal/terraform/datadir"
	"github.com/hashicorp/terraform-ls/internal/terraform/exec"
	tfaddr "github.com/hashicorp/terraform-registry-address"
	tfmod "github.com/hashicorp/terraform-schema/module"
)

type RootModulesFeature struct {
	store    *state.RootStore
	eventbus *eventbus.EventBus
	stopFunc context.CancelFunc
	logger   *log.Logger

	tfExecFactory        exec.ExecutorFactory
	jobStore             *globalState.JobStore
	providerSchemasStore *globalState.ProviderSchemaStore
	fs                   jobs.ReadOnlyFS
}

func NewRootModulesFeature(eventbus *eventbus.EventBus, tfExecFactory exec.ExecutorFactory, jobStore *globalState.JobStore, providerSchemasStore *globalState.ProviderSchemaStore, changeStore *globalState.ChangeStore, fs jobs.ReadOnlyFS) (*RootModulesFeature, error) {
	store, err := state.NewRootStore(changeStore)
	if err != nil {
		return nil, err
	}
	discardLogger := log.New(io.Discard, "", 0)

	return &RootModulesFeature{
		store:                store,
		eventbus:             eventbus,
		stopFunc:             func() {},
		logger:               discardLogger,
		tfExecFactory:        tfExecFactory,
		jobStore:             jobStore,
		providerSchemasStore: providerSchemasStore,
		fs:                   fs,
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

func (f *RootModulesFeature) UpdateModManifest(path string, manifest *datadir.ModuleManifest, mErr error) error {
	return f.store.UpdateModManifest(path, manifest, mErr)
}

func (f *RootModulesFeature) Telemetry(path string) map[string]interface{} {
	properties := make(map[string]interface{})

	record, err := f.store.RootRecordByPath(path)
	if err != nil {
		return properties
	}

	if record.TerraformVersion != nil {
		properties["tfVersion"] = record.TerraformVersion.String()
	}
	if len(record.InstalledProviders) > 0 {
		installedProviders := make(map[string]string, 0)
		for pAddr, pv := range record.InstalledProviders {
			if telemetry.IsPublicProvider(pAddr) {
				versionString := ""
				if pv != nil {
					versionString = pv.String()
				}
				installedProviders[pAddr.String()] = versionString
				continue
			}

			// anonymize any unknown providers or the ones not publicly listed
			id, err := f.providerSchemasStore.GetProviderID(pAddr)
			if err != nil {
				continue
			}
			addr := fmt.Sprintf("unlisted/%s", id)
			installedProviders[addr] = ""
		}
		properties["installedProviders"] = installedProviders
	}

	return properties
}
