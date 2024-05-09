// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package eventbus

import "context"

// DocumentChangedEvent is an event that is triggered by the walker when a new
// directory is walked.
//
// Most features use this to create a state entry if the directory contains
// files relevant to them.
type DocumentChangedEvent struct {
	Context context.Context

	Path  string
}

func (n *EventBus) OnDocumentChanged(identifier string) <-chan DocumentChangedEvent {
	n.logger.Printf("bus: %q subscribed to OnDocumentChanged", identifier)
	return n.documentChangedTopic.Subscribe()
}

func (n *EventBus) DocumentChanged(e DocumentChangedEvent) {
	n.logger.Printf("bus: -> DocumentChanged %s", e.Path)
	n.documentChangedTopic.Publish(e)
}
