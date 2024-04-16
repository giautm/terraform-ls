// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package jobs

import (
	"context"
	"path/filepath"

	lsctx "github.com/hashicorp/terraform-ls/internal/context"
	"github.com/hashicorp/terraform-ls/internal/document"
	"github.com/hashicorp/terraform-ls/internal/flavors/stacks/state"
	"github.com/hashicorp/terraform-ls/internal/job"
	ilsp "github.com/hashicorp/terraform-ls/internal/lsp"
	"github.com/hashicorp/terraform-ls/internal/terraform/ast"
	op "github.com/hashicorp/terraform-ls/internal/terraform/module/operation"
	"github.com/hashicorp/terraform-ls/internal/terraform/parser"
	"github.com/hashicorp/terraform-ls/internal/uri"
)

/*
	Stacks Project:
		component1/
			main.tf
			outputs.tf
		component2/
			main.tf
			outputs.tf
		components.tfstack.hcl
		providers.tfstack.hcl
		variables.tfstack.hcl
		deployments.tfdeploy.hcl
		.terraform-version
		.terraform.local.hcl

	Stack configuration:
		All *.tfstack.hcl files in root directory are considered as stack configurations.

	Component
		each component is considered a terraform module  (have inputs and outputs) but
		each is a seperate *module runtime* and is independent of
		each other beyond their inputs and outputs

	Module Runtime
		stacks runtime wraps the module runtime for orchestrating changes across components
*/
	func ParseStack(
	ctx context.Context,
	fs ReadOnlyFS,
	stackStore *state.StacksStore,
	path string,
) error {
	stack, err := stackStore.StacksRecordByPath(path)
	if err != nil {
		return err
	}
	rpcContext := lsctx.DocumentContext(ctx)

	// TODO: Avoid parsing if the content matches existing AST

	// Avoid parsing if it is already in progress or already known
	if stack.StacksDiagnosticsState[ast.HCLParsingSource] != op.OpStateUnknown && !job.IgnoreState(ctx) {
		return job.StateNotChangedErr{Dir: document.DirHandleFromPath(path)}
	}

	var files ast.ModFiles
	var diags ast.ModDiags
	// Only parse the file that's being changed/opened, unless this is 1st-time parsing
	if stack.StacksDiagnosticsState[ast.HCLParsingSource] == op.OpStateLoaded &&
		rpcContext.IsDidChangeRequest() &&
		rpcContext.LanguageID == ilsp.Stacks.String() {
		// the file has already been parsed, so only examine this file and not the whole module
		err = stackStore.SetStacksDiagnosticsState(path, ast.HCLParsingSource, op.OpStateLoading)
		if err != nil {
			return err
		}

		filePath, err := uri.PathFromURI(rpcContext.URI)
		if err != nil {
			return err
		}
		fileName := filepath.Base(filePath)

		f, fDiags, err := parser.ParseModuleFile(fs, filePath)
		if err != nil {
			return err
		}
		existingFiles := stack.ParsedStacksFiles.Copy()
		existingFiles[ast.ModFilename(fileName)] = f
		files = existingFiles

		existingDiags, ok := stack.StacksDiagnostics[ast.HCLParsingSource]
		if !ok {
			existingDiags = make(ast.ModDiags)
		} else {
			existingDiags = existingDiags.Copy()
		}
		existingDiags[ast.ModFilename(fileName)] = fDiags
		diags = existingDiags
	} else {
		// this is the first time file is opened so parse the whole module
		err = stackStore.SetStacksDiagnosticsState(path, ast.HCLParsingSource, op.OpStateLoading)
		if err != nil {
			return err
		}

		files, diags, err = parser.ParseModuleFiles(fs, path)
	}

	if err != nil {
		return err
	}

	sErr := stackStore.UpdateParsedStacksFiles(path, files, err)
	if sErr != nil {
		return sErr
	}

	sErr = stackStore.UpdateStacksDiagnostics(path, ast.HCLParsingSource, diags)
	if sErr != nil {
		return sErr
	}

	return err
}
