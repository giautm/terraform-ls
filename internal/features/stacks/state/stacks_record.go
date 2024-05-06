// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package state

import (
	"github.com/hashicorp/terraform-ls/internal/features/stacks/ast"
	globalAst "github.com/hashicorp/terraform-ls/internal/terraform/ast"
)

type StacksRecord struct {
	path string

	ParsedStacksFiles ast.StacksFiles
	StacksParsingErr  error

	StacksDiagnostics      ast.SourceStacksDiags
	StacksDiagnosticsState globalAst.DiagnosticSourceState
}

func (s *StacksRecord) Path() string {
	return s.path
}

func newStacks(stacksPath string) *StacksRecord {
	return &StacksRecord{
		path: stacksPath,
	}
}

func (s *StacksRecord) Copy() *StacksRecord {
	return &StacksRecord{
		path:                   s.path,
		StacksDiagnosticsState: s.StacksDiagnosticsState.Copy(),
	}
}
