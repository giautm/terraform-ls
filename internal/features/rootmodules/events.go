// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package rootmodules

import (
	"context"

	"github.com/hashicorp/go-multierror"
	"github.com/hashicorp/terraform-ls/internal/document"
	"github.com/hashicorp/terraform-ls/internal/features/rootmodules/ast"
	"github.com/hashicorp/terraform-ls/internal/features/rootmodules/jobs"
	"github.com/hashicorp/terraform-ls/internal/job"
	"github.com/hashicorp/terraform-ls/internal/terraform/exec"
	op "github.com/hashicorp/terraform-ls/internal/terraform/module/operation"
)

func (f *RootModulesFeature) discover(path string, files []string) error {
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

func (f *RootModulesFeature) didOpen(ctx context.Context, dir document.DirHandle, languageID string) (job.IDs, error) {
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

	versionId, err := f.jobStore.EnqueueJob(ctx, job.Job{
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
	ids = append(ids, versionId)

	modManifestId, err := f.jobStore.EnqueueJob(ctx, job.Job{
		Dir: dir,
		Func: func(ctx context.Context) error {
			return jobs.ParseModuleManifest(ctx, f.fs, f.store, dir.Path())
		},
		Type: op.OpTypeParseModuleManifest.String(),
	})
	if err != nil {
		return ids, err
	}
	ids = append(ids, modManifestId)

	return ids, nil
}

func (f *RootModulesFeature) pluginLockChanged(ctx context.Context, modHandle document.DirHandle) (job.IDs, error) {
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
