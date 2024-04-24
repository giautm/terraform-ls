// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package variables

import (
	"context"

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

	return ids, nil
}
