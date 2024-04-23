// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package jobs

import (
	"context"

	lsctx "github.com/hashicorp/terraform-ls/internal/context"
	"github.com/hashicorp/terraform-ls/internal/document"
	"github.com/hashicorp/terraform-ls/internal/features/stacks/ast"
	"github.com/hashicorp/terraform-ls/internal/features/stacks/parser"
	"github.com/hashicorp/terraform-ls/internal/features/stacks/state"
	"github.com/hashicorp/terraform-ls/internal/job"
	ilsp "github.com/hashicorp/terraform-ls/internal/lsp"
	op "github.com/hashicorp/terraform-ls/internal/terraform/module/operation"
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

	var files ast.StacksFiles
	// var diags ast.StacksDiags
	// Only parse the file that's being changed/opened, unless this is 1st-time parsing
	if stack.StacksDiagnosticsState[ast.HCLParsingSource] == op.OpStateLoaded &&
		rpcContext.IsDidChangeRequest() &&
		rpcContext.LanguageID == ilsp.Stacks.String() {
		// the file has already been parsed, so only examine this file and not the whole module
		// err = stackStore.SetStacksDiagnosticsState(path, ast.HCLParsingSource, op.OpStateLoading)
		// if err != nil {
		// 	return err
		// }

		// filePath, err := uri.PathFromURI(rpcContext.URI)
		// if err != nil {
		// 	return err
		// }
		// fileName := filepath.Base(filePath)

		// f, fDiags, err := parser.ParseStacksFile(fs, filePath)
		// if err != nil {
		// 	return err
		// }
		// existingFiles := stack.ParsedStacksFiles.Copy()
		// existingFiles[ast.StacksFilename(fileName)] = f
		// files = existingFiles

		// existingDiags, ok := stack.StacksDiagnostics[ast.HCLParsingSource]
		// if !ok {
		// 	existingDiags = make(ast.StacksDiags)
		// } else {
		// 	existingDiags = existingDiags.Copy()
		// }
		// existingDiags[ast.StacksFilename(fileName)] = fDiags
		// diags = existingDiags
	} else {
		// this is the first time file is opened so parse the whole module
		err = stackStore.SetStacksDiagnosticsState(path, ast.HCLParsingSource, op.OpStateLoading)
		if err != nil {
			return err
		}

		files, _, err = parser.ParseStacksFiles(fs, path)
	}

	if err != nil {
		return err
	}

	sErr := stackStore.UpdateParsedStacksFiles(path, files, err)
	if sErr != nil {
		return sErr
	}

	// TODO: Update diagnostics
	// sErr = stackStore.UpdateStacksDiagnostics(path, ast.HCLParsingSource, diags)
	// if sErr != nil {
	// 	return sErr
	// }

	return err
}
