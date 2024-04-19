// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package eventbus

import "context"

type DidChangeEvent struct {
	Context context.Context

	Path       string
	LanguageID string
	// TODO? Filename string

	// IncludeSubmodules bool
}

func (n *EventBus) OnDidChange(identifier string) <-chan DidChangeEvent {
	n.logger.Printf("bus: %q subscribed to OnDidChange", identifier)
	return n.didChangeTopic.Subscribe()
}

func (n *EventBus) DidChange(e DidChangeEvent) {
	n.logger.Printf("bus: -> DidChange %s", e.Path)
	n.didChangeTopic.Publish(e)
}
