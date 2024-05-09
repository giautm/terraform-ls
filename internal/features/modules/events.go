// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package modules

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/hashicorp/go-multierror"
	lsctx "github.com/hashicorp/terraform-ls/internal/context"
	"github.com/hashicorp/terraform-ls/internal/document"
	"github.com/hashicorp/terraform-ls/internal/features/modules/ast"
	"github.com/hashicorp/terraform-ls/internal/features/modules/jobs"
	"github.com/hashicorp/terraform-ls/internal/job"
	"github.com/hashicorp/terraform-ls/internal/schemas"
	globalState "github.com/hashicorp/terraform-ls/internal/state"
	globalAst "github.com/hashicorp/terraform-ls/internal/terraform/ast"
	op "github.com/hashicorp/terraform-ls/internal/terraform/module/operation"
	tfmod "github.com/hashicorp/terraform-schema/module"
)

func (f *ModulesFeature) discover(path string, files []string) error {
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

func (f *ModulesFeature) didOpen(ctx context.Context, dir document.DirHandle, languageID string) (job.IDs, error) {
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

	validationOptions, err := lsctx.ValidationOptions(ctx)
	if err != nil {
		return ids, err
	}
	if validationOptions.EnableEnhancedValidation {
		_, err = f.jobStore.EnqueueJob(ctx, job.Job{
			Dir: dir,
			Func: func(ctx context.Context) error {
				return jobs.SchemaModuleValidation(ctx, f.store, f.rootFeature, dir.Path())
			},
			Type:        op.OpTypeSchemaModuleValidation.String(),
			DependsOn:   ids,
			IgnoreState: true,
		})
		if err != nil {
			return ids, err
		}

		_, err = f.jobStore.EnqueueJob(ctx, job.Job{
			Dir: dir,
			Func: func(ctx context.Context) error {
				return jobs.ReferenceValidation(ctx, f.store, f.rootFeature, dir.Path())
			},
			Type:        op.OpTypeReferenceValidation.String(),
			DependsOn:   ids,
			IgnoreState: true,
		})
		if err != nil {
			return ids, err
		}
	}

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

func (f *ModulesFeature) documentChanged(ctx context.Context, path string) (job.IDs, error) {
	ids := make(job.IDs, 0)

	modHandle := document.DirHandleFromPath(path)

	// ParseModuleConfiguration
	parseId, err := f.jobStore.EnqueueJob(ctx, job.Job{
		Dir: modHandle,
		Func: func(ctx context.Context) error {
			return jobs.ParseModuleConfiguration(ctx, f.fs, f.store, modHandle.Path())
		},
		Type:        op.OpTypeParseModuleConfiguration.String(),
		IgnoreState: true,
	})
	if err != nil {
		return ids, err
	}
	ids = append(ids, parseId)

	modIds, err := f.decodeModule(ctx, modHandle, job.IDs{parseId}, true)
	if err != nil {
		return ids, err
	}
	ids = append(ids, modIds...)

	// ParseVariables

	return ids, err // continue
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

func (f *ModulesFeature) decodeModule(ctx context.Context, modHandle document.DirHandle, dependsOn job.IDs, ignoreState bool) (job.IDs, error) {
	ids := make(job.IDs, 0)

	// Changes to a setting currently requires a LS restart, so the LS
	// setting context cannot change during the execution of a job. That's
	// why we can extract it here and use it in Defer.
	// See https://github.com/hashicorp/terraform-ls/issues/1008
	validationOptions, err := lsctx.ValidationOptions(ctx)
	if err != nil {
		return ids, err
	}

	metaId, err := f.jobStore.EnqueueJob(ctx, job.Job{
		Dir: modHandle,
		Func: func(ctx context.Context) error {
			return jobs.LoadModuleMetadata(ctx, f.store, modHandle.Path())
		},
		Type:        op.OpTypeLoadModuleMetadata.String(),
		DependsOn:   dependsOn,
		IgnoreState: ignoreState,
		Defer: func(ctx context.Context, jobErr error) (job.IDs, error) {
			ids := make(job.IDs, 0)
			if jobErr != nil {
				f.logger.Printf("loading module metadata returned error: %s", jobErr)
			}

			modCalls, mcErr := f.decodeDeclaredModuleCalls(ctx, modHandle, ignoreState)
			if mcErr != nil {
				f.logger.Printf("decoding declared module calls for %q failed: %s", modHandle.URI, mcErr)
				// We log the error but still continue scheduling other jobs
				// which are still valuable for the rest of the configuration
				// even if they may not have the data for module calls.
			}

			eSchemaId, err := f.jobStore.EnqueueJob(ctx, job.Job{
				Dir: modHandle,
				Func: func(ctx context.Context) error {
					return jobs.PreloadEmbeddedSchema(ctx, f.logger, schemas.FS, f.store, f.providerSchemasStore, modHandle.Path())
				},
				Type:        op.OpTypePreloadEmbeddedSchema.String(),
				IgnoreState: ignoreState,
			})
			if err != nil {
				return ids, err
			}
			ids = append(ids, eSchemaId)

			if validationOptions.EnableEnhancedValidation {
				_, err = f.jobStore.EnqueueJob(ctx, job.Job{
					Dir: modHandle,
					Func: func(ctx context.Context) error {
						return jobs.SchemaModuleValidation(ctx, f.store, f.rootFeature, modHandle.Path())
					},
					Type:        op.OpTypeSchemaModuleValidation.String(),
					DependsOn:   append(modCalls, eSchemaId),
					IgnoreState: ignoreState,
				})
				if err != nil {
					return ids, err
				}
			}

			refTargetsId, err := f.jobStore.EnqueueJob(ctx, job.Job{
				Dir: modHandle,
				Func: func(ctx context.Context) error {
					return jobs.DecodeReferenceTargets(ctx, f.store, f.rootFeature, modHandle.Path())
				},
				Type:        op.OpTypeDecodeReferenceTargets.String(),
				DependsOn:   job.IDs{eSchemaId},
				IgnoreState: ignoreState,
			})
			if err != nil {
				return ids, err
			}
			ids = append(ids, refTargetsId)

			refOriginsId, err := f.jobStore.EnqueueJob(ctx, job.Job{
				Dir: modHandle,
				Func: func(ctx context.Context) error {
					return jobs.DecodeReferenceOrigins(ctx, f.store, f.rootFeature, modHandle.Path())
				},
				Type:        op.OpTypeDecodeReferenceOrigins.String(),
				DependsOn:   append(modCalls, eSchemaId),
				IgnoreState: ignoreState,
			})
			if err != nil {
				return ids, err
			}
			ids = append(ids, refOriginsId)

			if validationOptions.EnableEnhancedValidation {
				_, err = f.jobStore.EnqueueJob(ctx, job.Job{
					Dir: modHandle,
					Func: func(ctx context.Context) error {
						return jobs.ReferenceValidation(ctx, f.store, f.rootFeature, modHandle.Path())
					},
					Type:        op.OpTypeReferenceValidation.String(),
					DependsOn:   job.IDs{refOriginsId, refTargetsId},
					IgnoreState: ignoreState,
				})
				if err != nil {
					return ids, err
				}
			}

			return ids, nil
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
				f.store, f.registryModuleStore, modHandle.Path())
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
