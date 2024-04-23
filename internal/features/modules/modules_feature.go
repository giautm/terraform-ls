// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package variables

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/hashicorp/go-multierror"
	"github.com/hashicorp/hcl-lang/decoder"
	"github.com/hashicorp/hcl-lang/lang"
	"github.com/hashicorp/terraform-ls/internal/document"
	"github.com/hashicorp/terraform-ls/internal/eventbus"
	"github.com/hashicorp/terraform-ls/internal/features/modules/ast"
	fdecoder "github.com/hashicorp/terraform-ls/internal/features/modules/decoder"
	"github.com/hashicorp/terraform-ls/internal/features/modules/jobs"
	"github.com/hashicorp/terraform-ls/internal/features/modules/state"
	"github.com/hashicorp/terraform-ls/internal/job"
	"github.com/hashicorp/terraform-ls/internal/registry"
	"github.com/hashicorp/terraform-ls/internal/schemas"
	globalState "github.com/hashicorp/terraform-ls/internal/state"
	globalAst "github.com/hashicorp/terraform-ls/internal/terraform/ast"
	op "github.com/hashicorp/terraform-ls/internal/terraform/module/operation"
	tfmod "github.com/hashicorp/terraform-schema/module"
)

type ModulesFeature struct {
	store    *state.ModuleStore
	eventbus *eventbus.EventBus
	stopFunc context.CancelFunc
	logger   *log.Logger

	rootFeature          fdecoder.RootReader
	jobStore             *globalState.JobStore
	providerSchemasStore *globalState.ProviderSchemaStore
	registryModuleStore  *globalState.RegistryModuleStore

	registryClient registry.Client
	fs             jobs.ReadOnlyFS
}

func NewModulesFeature(eventbus *eventbus.EventBus, jobStore *globalState.JobStore, providerSchemasStore *globalState.ProviderSchemaStore, registryModuleStore *globalState.RegistryModuleStore, fs jobs.ReadOnlyFS, rootFeature fdecoder.RootReader) (*ModulesFeature, error) {
	store, err := state.NewModuleStore(providerSchemasStore, registryModuleStore)
	if err != nil {
		return nil, err
	}
	discardLogger := log.New(io.Discard, "", 0)

	return &ModulesFeature{
		store:                store,
		eventbus:             eventbus,
		stopFunc:             func() {},
		logger:               discardLogger,
		jobStore:             jobStore,
		rootFeature:          rootFeature,
		providerSchemasStore: providerSchemasStore,
		registryModuleStore:  registryModuleStore,
		fs:                   fs,
	}, nil
}

func (f *ModulesFeature) SetLogger(logger *log.Logger) {
	f.logger = logger
	f.store.SetLogger(logger)
}

func (f *ModulesFeature) Start(ctx context.Context) {
	ctx, cancelFunc := context.WithCancel(ctx)
	f.stopFunc = cancelFunc

	didOpen := f.eventbus.OnDidOpen("feature.modules")
	didChange := f.eventbus.OnDidChange("feature.modules")
	discover := f.eventbus.OnDiscover("feature.modules")
	go func() {
		for {
			select {
			case didOpen := <-didOpen:
				// TODO collect errors
				f.DidOpen(didOpen.Context, didOpen.Dir, didOpen.LanguageID)
			case didChange := <-didChange:
				// TODO move into own handler
				// TODO collect errors
				f.DidOpen(didChange.Context, didChange.Dir, didChange.LanguageID)
			case discover := <-discover:
				// TODO collect errors
				f.Discover(discover.Path, discover.Files)

			case <-ctx.Done():
				return
			}
		}
	}()
}

func (f *ModulesFeature) Stop() {
	f.stopFunc()
	f.logger.Print("stopped modules feature")
}

func (f *ModulesFeature) Discover(path string, files []string) error {
	for _, file := range files {
		if ast.IsModuleFilename(file) && !globalAst.IsIgnoredFile(file) {
			f.logger.Printf("discovered module file in %s", path)

			err := f.store.AddIfNotExists(path)
			if err != nil {
				return err
			}

			break
		}
	}

	return nil
}

func (f *ModulesFeature) DidOpen(ctx context.Context, dir document.DirHandle, languageID string) (job.IDs, error) {
	ids := make(job.IDs, 0)
	path := dir.Path()
	f.logger.Printf("did open %q %q", path, languageID)

	// We need to decide if the path is relevant to us. It can be relevant because
	// a) the walker discovered module files and created a state entry for them
	// b) the opened file is a module file
	//
	// Add to state if language ID matches
	if languageID == "terraform" {
		err := f.store.AddIfNotExists(path)
		if err != nil {
			return ids, err
		}
	}

	// Schedule jobs if state entry exists
	hasModuleRecord := f.store.Exists(path)
	if !hasModuleRecord {
		return ids, nil
	}

	parseId, err := f.jobStore.EnqueueJob(ctx, job.Job{
		Dir: dir,
		Func: func(ctx context.Context) error {
			return jobs.ParseModuleConfiguration(ctx, f.fs, f.store, path)
		},
		Type:        op.OpTypeParseModuleConfiguration.String(),
		IgnoreState: true,
	})
	if err != nil {
		return ids, err
	}
	ids = append(ids, parseId)

	metaId, err := f.jobStore.EnqueueJob(ctx, job.Job{
		Dir: dir,
		Func: func(ctx context.Context) error {
			return jobs.LoadModuleMetadata(ctx, f.store, path)
		},
		Type:        op.OpTypeLoadModuleMetadata.String(),
		DependsOn:   job.IDs{parseId},
		IgnoreState: true,
		Defer: func(ctx context.Context, jobErr error) (job.IDs, error) {
			deferIds := make(job.IDs, 0)
			if jobErr != nil {
				f.logger.Printf("loading module metadata returned error: %s", jobErr)
			}

			modCalls, mcErr := f.decodeDeclaredModuleCalls(ctx, dir, true)
			if mcErr != nil {
				f.logger.Printf("decoding declared module calls for %q failed: %s", dir.URI, mcErr)
				// We log the error but still continue scheduling other jobs
				// which are still valuable for the rest of the configuration
				// even if they may not have the data for module calls.
			}

			eSchemaId, err := f.jobStore.EnqueueJob(ctx, job.Job{
				Dir: dir,
				Func: func(ctx context.Context) error {
					return jobs.PreloadEmbeddedSchema(ctx, f.logger, schemas.FS, f.store, f.providerSchemasStore, path)
				},
				Type:        op.OpTypePreloadEmbeddedSchema.String(),
				IgnoreState: true,
			})
			if err != nil {
				return deferIds, err
			}
			deferIds = append(deferIds, eSchemaId)

			refTargetsId, err := f.jobStore.EnqueueJob(ctx, job.Job{
				Dir: dir,
				Func: func(ctx context.Context) error {
					return jobs.DecodeReferenceTargets(ctx, f.store, f.rootFeature, path)
				},
				Type:        op.OpTypeDecodeReferenceTargets.String(),
				DependsOn:   job.IDs{eSchemaId},
				IgnoreState: true,
			})
			if err != nil {
				return deferIds, err
			}
			deferIds = append(deferIds, refTargetsId)

			refOriginsId, err := f.jobStore.EnqueueJob(ctx, job.Job{
				Dir: dir,
				Func: func(ctx context.Context) error {
					return jobs.DecodeReferenceOrigins(ctx, f.store, f.rootFeature, path)
				},
				Type:        op.OpTypeDecodeReferenceOrigins.String(),
				DependsOn:   append(modCalls, eSchemaId),
				IgnoreState: true,
			})
			if err != nil {
				return deferIds, err
			}
			deferIds = append(deferIds, refOriginsId)

			return deferIds, nil
		},
	})
	if err != nil {
		return ids, err
	}
	ids = append(ids, metaId)

	// This job may make an HTTP request, and we schedule it in
	// the low-priority queue, so we don't want to wait for it.
	_, err = f.jobStore.EnqueueJob(ctx, job.Job{
		Dir: dir,
		Func: func(ctx context.Context) error {
			return jobs.GetModuleDataFromRegistry(ctx, f.registryClient,
				f.store, f.registryModuleStore, path)
		},
		Priority:  job.LowPriority,
		DependsOn: job.IDs{metaId},
		Type:      op.OpTypeGetModuleDataFromRegistry.String(),
	})
	if err != nil {
		return ids, err
	}

	return ids, nil
}

func (f *ModulesFeature) decodeDeclaredModuleCalls(ctx context.Context, dir document.DirHandle, ignoreState bool) (job.IDs, error) {
	jobIds := make(job.IDs, 0)

	declared, err := f.store.DeclaredModuleCalls(dir.Path())
	if err != nil {
		return jobIds, err
	}

	var errs *multierror.Error

	f.logger.Printf("indexing declared module calls for %q: %d", dir.URI, len(declared))
	for _, mc := range declared {
		localSource, ok := mc.SourceAddr.(tfmod.LocalSourceAddr)
		if !ok {
			continue
		}
		mcPath := filepath.Join(dir.Path(), filepath.FromSlash(localSource.String()))

		fi, err := os.Stat(mcPath)
		if err != nil || !fi.IsDir() {
			multierror.Append(errs, err)
			continue
		}

		mcIgnoreState := ignoreState
		err = f.store.Add(mcPath) // TODO! revisit for language IDs
		if err != nil {
			alreadyExistsErr := &globalState.AlreadyExistsError{}
			if errors.As(err, &alreadyExistsErr) {
				mcIgnoreState = false
			} else {
				multierror.Append(errs, err)
				continue
			}
		}

		mcHandle := document.DirHandleFromPath(mcPath)
		mcJobIds, mcErr := f.decodeModuleAtPath(ctx, mcHandle, mcIgnoreState)
		jobIds = append(jobIds, mcJobIds...)
		multierror.Append(errs, mcErr)
	}

	return jobIds, errs.ErrorOrNil()
}

func (f *ModulesFeature) decodeModuleAtPath(ctx context.Context, dir document.DirHandle, ignoreState bool) (job.IDs, error) {
	var errs *multierror.Error
	jobIds := make(job.IDs, 0)
	refCollectionDeps := make(job.IDs, 0)

	parseId, err := f.jobStore.EnqueueJob(ctx, job.Job{
		Dir: dir,
		Func: func(ctx context.Context) error {
			return jobs.ParseModuleConfiguration(ctx, f.fs, f.store, dir.Path())
		},
		Type:        op.OpTypeParseModuleConfiguration.String(),
		IgnoreState: ignoreState,
	})
	if err != nil {
		multierror.Append(errs, err)
	} else {
		jobIds = append(jobIds, parseId)
		refCollectionDeps = append(refCollectionDeps, parseId)
	}

	var metaId job.ID
	if parseId != "" {
		metaId, err = f.jobStore.EnqueueJob(ctx, job.Job{
			Dir:  dir,
			Type: op.OpTypeLoadModuleMetadata.String(),
			Func: func(ctx context.Context) error {
				return jobs.LoadModuleMetadata(ctx, f.store, dir.Path())
			},
			DependsOn:   job.IDs{parseId},
			IgnoreState: ignoreState,
		})
		if err != nil {
			multierror.Append(errs, err)
		} else {
			jobIds = append(jobIds, metaId)
			refCollectionDeps = append(refCollectionDeps, metaId)
		}

		eSchemaId, err := f.jobStore.EnqueueJob(ctx, job.Job{
			Dir: dir,
			Func: func(ctx context.Context) error {
				return jobs.PreloadEmbeddedSchema(ctx, f.logger, schemas.FS, f.store, f.providerSchemasStore, dir.Path())
			},
			Type:        op.OpTypePreloadEmbeddedSchema.String(),
			DependsOn:   job.IDs{metaId},
			IgnoreState: ignoreState,
		})
		if err != nil {
			multierror.Append(errs, err)
		} else {
			jobIds = append(jobIds, eSchemaId)
			refCollectionDeps = append(refCollectionDeps, eSchemaId)
		}
	}

	if parseId != "" {
		ids, err := f.collectReferences(ctx, dir, refCollectionDeps, ignoreState)
		if err != nil {
			multierror.Append(errs, err)
		} else {
			jobIds = append(jobIds, ids...)
		}
	}

	// TODO: run variable related jobs IF there are variable files

	return jobIds, errs.ErrorOrNil()
}

func (f *ModulesFeature) collectReferences(ctx context.Context, dir document.DirHandle, dependsOn job.IDs, ignoreState bool) (job.IDs, error) {
	ids := make(job.IDs, 0)

	var errs *multierror.Error

	id, err := f.jobStore.EnqueueJob(ctx, job.Job{
		Dir: dir,
		Func: func(ctx context.Context) error {
			return jobs.DecodeReferenceTargets(ctx, f.store, f.rootFeature, dir.Path())
		},
		Type:        op.OpTypeDecodeReferenceTargets.String(),
		DependsOn:   dependsOn,
		IgnoreState: ignoreState,
	})
	if err != nil {
		errs = multierror.Append(errs, err)
	} else {
		ids = append(ids, id)
	}

	id, err = f.jobStore.EnqueueJob(ctx, job.Job{
		Dir: dir,
		Func: func(ctx context.Context) error {
			return jobs.DecodeReferenceOrigins(ctx, f.store, f.rootFeature, dir.Path())
		},
		Type:        op.OpTypeDecodeReferenceOrigins.String(),
		DependsOn:   dependsOn,
		IgnoreState: ignoreState,
	})
	if err != nil {
		errs = multierror.Append(errs, err)
	} else {
		ids = append(ids, id)
	}

	return ids, errs.ErrorOrNil()
}

func (f *ModulesFeature) PathContext(path lang.Path) (*decoder.PathContext, error) {
	pathReader := &fdecoder.PathReader{
		StateReader: f.store,
		RootReader:  f.rootFeature,
	}

	return pathReader.PathContext(path)
}

func (f *ModulesFeature) Paths(ctx context.Context) []lang.Path {
	paths := make([]lang.Path, 0)

	// TODO

	return paths
}
