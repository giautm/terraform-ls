// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package variables

import (
	"context"

	lsctx "github.com/hashicorp/terraform-ls/internal/context"
	"github.com/hashicorp/terraform-ls/internal/document"
	"github.com/hashicorp/terraform-ls/internal/features/variables/ast"
	"github.com/hashicorp/terraform-ls/internal/features/variables/jobs"
	"github.com/hashicorp/terraform-ls/internal/job"
	op "github.com/hashicorp/terraform-ls/internal/terraform/module/operation"
)

func (f *VariablesFeature) discover(path string, files []string) error {
	for _, file := range files {
		if ast.IsVarsFilename(file) {
			f.logger.Printf("discovered variable file in %s", path)

			err := f.store.AddIfNotExists(path)
			if err != nil {
				return err
			}

			break
		}
	}

	return nil
}

func (f *VariablesFeature) didOpen(ctx context.Context, dir document.DirHandle, languageID string) (job.IDs, error) {
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

	validationOptions, err := lsctx.ValidationOptions(ctx)
	if err != nil {
		return ids, err
	}
	if validationOptions.EnableEnhancedValidation {
		wCh, moduleReady, err := f.moduleFeature.MetadataReady(dir)
		if err != nil {
			return ids, err
		}
		if !moduleReady {
			select {
			// Wait for module to be ready
			case <-wCh:
			case <-ctx.Done(): // TODO can we cancel via context here?
				return ids, ctx.Err()
			}
		}

		_, err = f.jobStore.EnqueueJob(ctx, job.Job{
			Dir: dir,
			Func: func(ctx context.Context) error {
				return jobs.SchemaVariablesValidation(ctx, f.store, f.moduleFeature, path)
			},
			Type:        op.OpTypeSchemaVarsValidation.String(),
			DependsOn:   job.IDs{parseVarsId},
			IgnoreState: true,
		})
		if err != nil {
			return ids, err
		}
	}

	return ids, nil
}

func (f *VariablesFeature) documentChanged(ctx context.Context, path string) (job.IDs, error) {
	ids := make(job.IDs, 0)

	modHandle := document.DirHandleFromPath(path)

	parseVarsId, err := f.jobStore.EnqueueJob(ctx, job.Job{
		Dir: modHandle,
		Func: func(ctx context.Context) error {
			return jobs.ParseVariables(ctx, f.fs, f.store, modHandle.Path())
		},
		Type:        op.OpTypeParseVariables.String(),
		IgnoreState: true,
	})
	if err != nil {
		return ids, err
	}
	ids = append(ids, parseVarsId)

	validationOptions, err := lsctx.ValidationOptions(ctx)
	if err != nil {
		return ids, err
	}

	// TODO: depends on idx.decodeModule
	if validationOptions.EnableEnhancedValidation {
		_, err = f.jobStore.EnqueueJob(ctx, job.Job{
			Dir: modHandle,
			Func: func(ctx context.Context) error {
				return jobs.SchemaVariablesValidation(ctx, f.store, f.moduleFeature, modHandle.Path())
			},
			Type:        op.OpTypeSchemaVarsValidation.String(),
			// TODO: DependsOn:   append(modIds, parseVarsId),
			IgnoreState: true,
		})
		if err != nil {
			return ids, err
		}
	}

	varsRefsId, err := f.jobStore.EnqueueJob(ctx, job.Job{
		Dir: modHandle,
		Func: func(ctx context.Context) error {
			return jobs.DecodeVarsReferences(ctx, f.store, f.moduleFeature, modHandle.Path())
		},
		Type:        op.OpTypeDecodeVarsReferences.String(),
		DependsOn:   job.IDs{parseVarsId},
		IgnoreState: true,
	})
	if err != nil {
		return ids, err
	}
	ids = append(ids, varsRefsId)

	return ids, err // continue
}
