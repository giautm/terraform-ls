// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package handlers

import (
	"context"

	"github.com/hashicorp/terraform-ls/internal/eventbus"
	lsp "github.com/hashicorp/terraform-ls/internal/protocol"
)

func (svc *service) DidChangeWatchedFiles(ctx context.Context, params lsp.DidChangeWatchedFilesParams) error {
	svc.logger.Printf("Received changes %q", len(params.Changes))
	for _, change := range params.Changes {
		svc.logger.Printf("Received change event for %q: %s", change.Type, change.URI)
		svc.eventBus.DidChangeWatched(eventbus.DidChangeWatchedEvent{
			Context:    ctx, // We pass the context for data here
			FileURI:    string(change.URI),
			ChangeType: change.Type,
		})
	}

	return nil
}
