// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package variables

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"

	"github.com/hashicorp/go-multierror"
	"github.com/hashicorp/terraform-ls/internal/document"
	"github.com/hashicorp/terraform-ls/internal/flavors/modules/jobs"
	"github.com/hashicorp/terraform-ls/internal/flavors/modules/state"
	"github.com/hashicorp/terraform-ls/internal/job"
	"github.com/hashicorp/terraform-ls/internal/registry"
	"github.com/hashicorp/terraform-ls/internal/schemas"
	globalState "github.com/hashicorp/terraform-ls/internal/state"
	op "github.com/hashicorp/terraform-ls/internal/terraform/module/operation"
	tfmodule "github.com/hashicorp/terraform-schema/module"
)

type ModulesFlavor struct {
	store  *state.ModuleStore
	logger *log.Logger

	jobStore              *globalState.JobStore
	providerSchemasStore  *globalState.ProviderSchemaStore
	registryModuleStore   *globalState.RegistryModuleStore
	rootStore             *globalState.RootStore
	terraformVersionStore *globalState.TerraformVersionStore

	registryClient registry.Client
	fs             jobs.ReadOnlyFS
}

func NewModulesFlavor(logger *log.Logger, jobStore *globalState.JobStore, providerSchemasStore *globalState.ProviderSchemaStore, registryModuleStore *globalState.RegistryModuleStore, rootStore *globalState.RootStore, terraformVersionStore *globalState.TerraformVersionStore, fs jobs.ReadOnlyFS) (*ModulesFlavor, error) {
	store, err := state.NewModuleStore(logger, providerSchemasStore, registryModuleStore, rootStore, terraformVersionStore)
	if err != nil {
		return nil, err
	}

	return &ModulesFlavor{
		store:                 store,
		logger:                logger,
		jobStore:              jobStore,
		providerSchemasStore:  providerSchemasStore,
		registryModuleStore:   registryModuleStore,
		rootStore:             rootStore,
		terraformVersionStore: terraformVersionStore,
		fs:                    fs,
	}, nil
}

func (f *ModulesFlavor) DidOpen(ctx context.Context, path string, languageID string) (job.IDs, error) {
	ids := make(job.IDs, 0)

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

	modHandle := document.DirHandleFromPath(path)
	parseId, err := f.jobStore.EnqueueJob(ctx, job.Job{
		Dir: modHandle,
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
		Dir: modHandle,
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

			modCalls, mcErr := f.decodeDeclaredModuleCalls(ctx, modHandle, true)
			if mcErr != nil {
				f.logger.Printf("decoding declared module calls for %q failed: %s", modHandle.URI, mcErr)
				// We log the error but still continue scheduling other jobs
				// which are still valuable for the rest of the configuration
				// even if they may not have the data for module calls.
			}

			eSchemaId, err := f.jobStore.EnqueueJob(ctx, job.Job{
				Dir: modHandle,
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
				Dir: modHandle,
				Func: func(ctx context.Context) error {
					return jobs.DecodeReferenceTargets(ctx, f.store, path)
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
				Dir: modHandle,
				Func: func(ctx context.Context) error {
					return jobs.DecodeReferenceOrigins(ctx, f.store, path)
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
		Dir: modHandle,
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

func (f *ModulesFlavor) decodeDeclaredModuleCalls(ctx context.Context, modHandle document.DirHandle, ignoreState bool) (job.IDs, error) {
	jobIds := make(job.IDs, 0)

	declared, err := f.store.DeclaredModuleCalls(modHandle.Path())
	if err != nil {
		return jobIds, err
	}

	var errs *multierror.Error

	f.logger.Printf("indexing declared module calls for %q: %d", modHandle.URI, len(declared))
	for _, mc := range declared {
		localSource, ok := mc.SourceAddr.(tfmodule.LocalSourceAddr)
		if !ok {
			continue
		}
		mcPath := filepath.Join(modHandle.Path(), filepath.FromSlash(localSource.String()))

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

func (f *ModulesFlavor) decodeModuleAtPath(ctx context.Context, modHandle document.DirHandle, ignoreState bool) (job.IDs, error) {
	var errs *multierror.Error
	jobIds := make(job.IDs, 0)
	refCollectionDeps := make(job.IDs, 0)

	parseId, err := f.jobStore.EnqueueJob(ctx, job.Job{
		Dir: modHandle,
		Func: func(ctx context.Context) error {
			return jobs.ParseModuleConfiguration(ctx, f.fs, f.store, modHandle.Path())
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
			Dir:  modHandle,
			Type: op.OpTypeLoadModuleMetadata.String(),
			Func: func(ctx context.Context) error {
				return jobs.LoadModuleMetadata(ctx, f.store, modHandle.Path())
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
			Dir: modHandle,
			Func: func(ctx context.Context) error {
				return jobs.PreloadEmbeddedSchema(ctx, f.logger, schemas.FS, f.store, f.providerSchemasStore, modHandle.Path())
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
		ids, err := f.collectReferences(ctx, modHandle, refCollectionDeps, ignoreState)
		if err != nil {
			multierror.Append(errs, err)
		} else {
			jobIds = append(jobIds, ids...)
		}
	}

	// TODO: run variable related jobs IF there are variable files

	return jobIds, errs.ErrorOrNil()
}

func (f *ModulesFlavor) collectReferences(ctx context.Context, modHandle document.DirHandle, dependsOn job.IDs, ignoreState bool) (job.IDs, error) {
	ids := make(job.IDs, 0)

	var errs *multierror.Error

	id, err := f.jobStore.EnqueueJob(ctx, job.Job{
		Dir: modHandle,
		Func: func(ctx context.Context) error {
			return jobs.DecodeReferenceTargets(ctx, f.store, modHandle.Path())
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
		Dir: modHandle,
		Func: func(ctx context.Context) error {
			return jobs.DecodeReferenceOrigins(ctx, f.store, modHandle.Path())
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
