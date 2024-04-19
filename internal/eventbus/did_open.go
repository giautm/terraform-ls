// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package eventbus

import "context"

type DidOpenEvent struct {
	Context context.Context

	Path       string
	LanguageID string

	// IncludeSubmodules bool
}

func (n *EventBus) OnDidOpen(identifier string) <-chan DidOpenEvent {
	n.logger.Printf("bus: %q subscribed to OnDidOpen", identifier)
	return n.didOpenTopic.Subscribe()
}

func (n *EventBus) DidOpen(e DidOpenEvent) {
	n.logger.Printf("bus: -> DidOpen %s", e.Path)
	n.didOpenTopic.Publish(e)
}
