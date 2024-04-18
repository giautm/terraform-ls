// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package handlers

import (
	"context"
	"fmt"

	"github.com/creachadair/jrpc2"
	"github.com/hashicorp/terraform-ls/internal/document"
	"github.com/hashicorp/terraform-ls/internal/eventbus"
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

	svc.eventBus.DidOpen(eventbus.DidOpenEvent{
		Context:    ctx,
		Path:       dh.Dir.Path(),
		LanguageID: params.TextDocument.LanguageID,
	})

	recordType := ast.RecordTypeFromLanguageID(params.TextDocument.LanguageID)
	if recordType == ast.RecordTypeStacks {
		svc.logger.Printf("opened stack %s: %s", recordType, dh.Dir.Path())
		return nil
	}

	err = svc.recordStores.AddIfNotExists(dh.Dir.Path(), recordType)
	if err != nil {
		return err
	}

	svc.logger.Printf("opened %s: %s", recordType, dh.Dir.Path())

	modHandle := document.DirHandleFromPath(dh.Dir.Path())
	if svc.singleFileMode {
		// TODO
		err = svc.stateStore.WalkerPaths.EnqueueDir(ctx, modHandle)
		if err != nil {
			return err
		}
	}

	return nil
}
