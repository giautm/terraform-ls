// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package variables

import (
	"context"
	"log"

	"github.com/hashicorp/terraform-ls/internal/document"
	"github.com/hashicorp/terraform-ls/internal/flavors/modules/jobs"
	"github.com/hashicorp/terraform-ls/internal/flavors/modules/state"
	"github.com/hashicorp/terraform-ls/internal/job"
	"github.com/hashicorp/terraform-ls/internal/schemas"
	globalState "github.com/hashicorp/terraform-ls/internal/state"
	op "github.com/hashicorp/terraform-ls/internal/terraform/module/operation"
)

type ModulesFlavor struct {
	store  *state.ModuleStore
	logger *log.Logger

	jobStore *globalState.JobStore
	fs       jobs.ReadOnlyFS
}

func NewModulesFlavor(logger *log.Logger, jobStore *globalState.JobStore, fs jobs.ReadOnlyFS) (*ModulesFlavor, error) {
	store, err := state.NewModuleStore(logger)
	if err != nil {
		return nil, err
	}

	return &ModulesFlavor{
		store:    store,
		logger:   logger,
		jobStore: jobStore,
		fs:       fs,
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

			modCalls, mcErr := idx.decodeDeclaredModuleCalls(ctx, modHandle, true)
			if mcErr != nil {
				f.logger.Printf("decoding declared module calls for %q failed: %s", modHandle.URI, mcErr)
				// We log the error but still continue scheduling other jobs
				// which are still valuable for the rest of the configuration
				// even if they may not have the data for module calls.
			}

			eSchemaId, err := f.jobStore.EnqueueJob(ctx, job.Job{
				Dir: modHandle,
				Func: func(ctx context.Context) error {
					return jobs.PreloadEmbeddedSchema(ctx, f.logger, schemas.FS, f.store, idx.recordStores.ProviderSchemas, path)
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
					return jobs.DecodeReferenceTargets(ctx, f.store, idx.recordStores, path)
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
					return jobs.DecodeReferenceOrigins(ctx, f.store, idx.recordStores, path)
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
			return jobs.GetModuleDataFromRegistry(ctx, idx.registryClient,
				f.store, idx.recordStores.RegistryModules, path)
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
