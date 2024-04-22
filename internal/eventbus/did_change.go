// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package eventbus

import (
	"context"

	"github.com/hashicorp/terraform-ls/internal/document"
)

type DidChangeEvent struct {
	Context context.Context

	Dir        document.DirHandle
	LanguageID string
	// TODO? Filename string

	// IncludeSubmodules bool
}

func (n *EventBus) OnDidChange(identifier string) <-chan DidChangeEvent {
	n.logger.Printf("bus: %q subscribed to OnDidChange", identifier)
	return n.didChangeTopic.Subscribe()
}

func (n *EventBus) DidChange(e DidChangeEvent) {
	n.logger.Printf("bus: -> DidChange %s", e.Dir)
	n.didChangeTopic.Publish(e)
}
