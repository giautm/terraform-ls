// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package state

import "github.com/hashicorp/terraform-ls/internal/terraform/ast"

type StacksRecord struct {
	path string

	ParsedStacksFiles ast.ModFiles
	StacksParsingErr  error

	StacksDiagnostics      ast.SourceModDiags
	StacksDiagnosticsState ast.DiagnosticSourceState
}

func newModule(modPath string) *StacksRecord {
	return &StacksRecord{
		path: modPath,
	}
}

func (s *StacksRecord) Copy() *StacksRecord {
	return &StacksRecord{
		path:                   s.path,
		StacksDiagnosticsState: s.StacksDiagnosticsState.Copy(),
	}
}
