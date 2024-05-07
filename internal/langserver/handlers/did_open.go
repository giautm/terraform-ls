// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package handlers

import (
	"context"
	"fmt"

	"github.com/creachadair/jrpc2"
	"github.com/hashicorp/terraform-ls/internal/document"
	lsp "github.com/hashicorp/terraform-ls/internal/protocol"
	"github.com/hashicorp/terraform-ls/internal/terraform/ast"
	"github.com/hashicorp/terraform-ls/internal/uri"
)

func (svc *service) TextDocumentDidOpen(ctx context.Context, params lsp.DidOpenTextDocumentParams) error {
	docURI := string(params.TextDocument.URI)

	// URIs are always checked during initialize request, but
	// we still allow single-file mode, therefore invalid URIs
	// can still land here, so we check for those.
	if !uri.IsURIValid(docURI) {
		jrpc2.ServerFromContext(ctx).Notify(ctx, "window/showMessage", &lsp.ShowMessageParams{
			Type: lsp.Warning,
			Message: fmt.Sprintf("Ignoring workspace folder (unsupport or invalid URI) %s."+
				" This is most likely bug, please report it.", docURI),
		})
		return fmt.Errorf("invalid URI: %s", docURI)
	}

	dh := document.HandleFromURI(docURI)

	err := svc.stateStore.DocumentStore.OpenDocument(dh, params.TextDocument.LanguageID,
		int(params.TextDocument.Version), []byte(params.TextDocument.Text))
	if err != nil {
		return err
	}

	svc.flavors.Modules.DidOpen(ctx, dh.Dir.Path(), params.TextDocument.LanguageID)
	svc.flavors.Variables.DidOpen(ctx, dh.Dir.Path(), params.TextDocument.LanguageID)

	recordType := ast.RecordTypeFromLanguageID(params.TextDocument.LanguageID)
	err = svc.recordStores.AddIfNotExists(dh.Dir.Path(), recordType)
	if err != nil {
		return err
	}

	svc.logger.Printf("opened %s: %s", recordType, dh.Dir.Path())

	// We reparse because the file being opened may not match
	// (originally parsed) content on the disk
	// TODO: Do this only if we can verify the file differs?
	modHandle := document.DirHandleFromPath(dh.Dir.Path())
	jobIds, err := svc.indexer.DocumentOpened(ctx, modHandle)
	if err != nil {
		return err
	}

	if svc.singleFileMode {
		err = svc.stateStore.WalkerPaths.EnqueueDir(ctx, modHandle)
		if err != nil {
			return err
		}
	}

	return svc.stateStore.JobStore.WaitForJobs(ctx, jobIds...)
}
