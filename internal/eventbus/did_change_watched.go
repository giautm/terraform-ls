// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package eventbus

import (
	"context"

	"github.com/hashicorp/terraform-ls/internal/protocol"
)

type DidChangeWatchedEvent struct {
	Context context.Context

	FileURI    string
	ChangeType protocol.FileChangeType
	// TODO? Filename string

	// IncludeSubmodules bool
}

func (n *EventBus) OnDidChangeWatched(identifier string) <-chan DidChangeWatchedEvent {
	n.logger.Printf("bus: %q subscribed to OnDidChangeWatched", identifier)
	return n.didChangeWatchedTopic.Subscribe()
}

func (n *EventBus) DidChangeWatched(e DidChangeWatchedEvent) {
	n.logger.Printf("bus: -> DidChangeWatched %s", e.FileURI)
	n.didChangeWatchedTopic.Publish(e)
}
