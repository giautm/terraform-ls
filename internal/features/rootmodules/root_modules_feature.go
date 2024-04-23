// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package rootmodules

import (
	"context"
	"io"
	"log"

	"github.com/hashicorp/go-multierror"
	"github.com/hashicorp/go-version"
	"github.com/hashicorp/terraform-ls/internal/document"
	"github.com/hashicorp/terraform-ls/internal/eventbus"
	"github.com/hashicorp/terraform-ls/internal/features/rootmodules/ast"
	"github.com/hashicorp/terraform-ls/internal/features/rootmodules/jobs"
	"github.com/hashicorp/terraform-ls/internal/features/rootmodules/state"
	"github.com/hashicorp/terraform-ls/internal/job"
	globalState "github.com/hashicorp/terraform-ls/internal/state"
	"github.com/hashicorp/terraform-ls/internal/terraform/exec"
	op "github.com/hashicorp/terraform-ls/internal/terraform/module/operation"
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
				f.DidOpen(open.Context, open.Dir, open.LanguageID)
			case discover := <-discover:
				// TODO collect errors
				f.Discover(discover.Path, discover.Files)

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

func (f *RootModulesFeature) Discover(path string, files []string) error {
	for _, file := range files {
		if ast.IsRootModuleFilename(file) {
			f.logger.Printf("discovered root module file in %s", path)

			err := f.store.AddIfNotExists(path)
			if err != nil {
				return err
			}

			break
		}
	}

	return nil
}

func (f *RootModulesFeature) DidOpen(ctx context.Context, dir document.DirHandle, languageID string) (job.IDs, error) {
	ids := make(job.IDs, 0)
	path := dir.Path()

	// There is no dedicated language id for root module related files
	// so we rely on the walker to discover root modules and add them to the
	// store during walking.

	// Schedule jobs if state entry exists
	hasModuleRecord := f.store.Exists(path)
	if !hasModuleRecord {
		return ids, nil
	}

	_, err := f.jobStore.EnqueueJob(ctx, job.Job{
		Dir: dir,
		Func: func(ctx context.Context) error {
			ctx = exec.WithExecutorFactory(ctx, f.tfExecFactory)
			return jobs.GetTerraformVersion(ctx, f.store, path)
		},
		Type: op.OpTypeGetTerraformVersion.String(),
	})
	if err != nil {
		return ids, nil
	}

	return ids, nil
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

func (f *RootModulesFeature) ModuleManifestChanged(ctx context.Context, modHandle document.DirHandle) (job.IDs, error) {
	ids := make(job.IDs, 0)

	modManifestId, err := f.jobStore.EnqueueJob(ctx, job.Job{
		Dir: modHandle,
		Func: func(ctx context.Context) error {
			return jobs.ParseModuleManifest(ctx, f.fs, f.store, modHandle.Path())
		},
		Type:        op.OpTypeParseModuleManifest.String(),
		IgnoreState: true,
		Defer: func(ctx context.Context, jobErr error) (job.IDs, error) {
			return nil, nil //idx.decodeInstalledModuleCalls(ctx, modHandle, true)
		},
	})
	if err != nil {
		return ids, err
	}
	ids = append(ids, modManifestId)

	return ids, nil
}

func (f *RootModulesFeature) PluginLockChanged(ctx context.Context, modHandle document.DirHandle) (job.IDs, error) {
	ids := make(job.IDs, 0)
	dependsOn := make(job.IDs, 0)
	var errs *multierror.Error

	pSchemaVerId, err := f.jobStore.EnqueueJob(ctx, job.Job{
		Dir: modHandle,
		Func: func(ctx context.Context) error {
			return jobs.ParseProviderVersions(ctx, f.fs, f.store, modHandle.Path())
		},
		IgnoreState: true,
		Type:        op.OpTypeParseProviderVersions.String(),
	})
	if err != nil {
		errs = multierror.Append(errs, err)
	} else {
		ids = append(ids, pSchemaVerId)
		dependsOn = append(dependsOn, pSchemaVerId)
	}

	pSchemaId, err := f.jobStore.EnqueueJob(ctx, job.Job{
		Dir: modHandle,
		Func: func(ctx context.Context) error {
			ctx = exec.WithExecutorFactory(ctx, f.tfExecFactory)
			return nil //module.ObtainSchema(ctx, idx.recordStores.Modules, idx.recordStores.ProviderSchemas, modHandle.Path())
		},
		IgnoreState: true,
		Type:        op.OpTypeObtainSchema.String(),
		DependsOn:   dependsOn,
	})
	if err != nil {
		errs = multierror.Append(errs, err)
	} else {
		ids = append(ids, pSchemaId)
	}

	return ids, errs.ErrorOrNil()
}
