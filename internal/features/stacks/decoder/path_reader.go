// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package decoder

import (
	"context"

	"github.com/hashicorp/hcl-lang/decoder"
	"github.com/hashicorp/hcl-lang/lang"
	"github.com/hashicorp/terraform-ls/internal/features/stacks/state"
	ilsp "github.com/hashicorp/terraform-ls/internal/lsp"
)

type StateReader interface {
	StacksRecordByPath(modPath string) (*state.StacksRecord, error)
	List() ([]*state.StacksRecord, error)
}

type PathReader struct {
	StateReader StateReader
}

var _ decoder.PathReader = &PathReader{}

func (pr *PathReader) Paths(ctx context.Context) []lang.Path {
	paths := make([]lang.Path, 0)

	stacksRecords, err := pr.StateReader.List()
	if err == nil {
		for _, record := range stacksRecords {
			paths = append(paths, lang.Path{
				Path:       record.Path(),
				LanguageID: ilsp.Stacks.String(),
			})
		}
	}

	return paths
}

// PathContext returns a PathContext for the given path based on the language ID.
func (pr *PathReader) PathContext(path lang.Path) (*decoder.PathContext, error) {
	mod, err := pr.StateReader.StacksRecordByPath(path.Path)
	if err != nil {
		return nil, err
	}
	return stacksPathContext(mod, pr.StateReader)
}
