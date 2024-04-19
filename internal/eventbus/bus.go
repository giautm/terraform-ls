// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package eventbus

import (
	"log"
	"sync"
)

const ChannelSize = 10

type EventBus struct {
	logger *log.Logger

	didOpenTopic   *Topic[DidOpenEvent]
	didChangeTopic *Topic[DidChangeEvent]
	discoverTopic  *Topic[DiscoverEvent]
}

func NewEventBus(log *log.Logger) *EventBus {
	return &EventBus{
		logger:         log,
		didOpenTopic:   NewTopic[DidOpenEvent](),
		didChangeTopic: NewTopic[DidChangeEvent](),
		discoverTopic:  NewTopic[DiscoverEvent](),
	}
}

// Topic represents a generic subscription topic
type Topic[T any] struct {
	subscribers [](chan T)
	mutex       sync.Mutex
}

// NewTopic creates a new topic
func NewTopic[T any]() *Topic[T] {
	return &Topic[T]{
		subscribers: make([](chan T), 0),
	}
}

// Subscribe adds a subscriber to a topic
func (eb *Topic[T]) Subscribe() <-chan T {
	ret := make(chan T, ChannelSize)
	eb.mutex.Lock()
	defer eb.mutex.Unlock()

	eb.subscribers = append(eb.subscribers, ret)
	return ret
}

// Unsubscribe removes a subscriber for a specific topic
func (eb *Topic[T]) Unsubscribe(subscriber <-chan T) {
	eb.mutex.Lock()
	defer eb.mutex.Unlock()
	newSubscribers := make([](chan T), 0, len(eb.subscribers)-1)
	for _, s := range eb.subscribers {
		if s != subscriber {
			newSubscribers = append(newSubscribers, s)
		}
	}
	eb.subscribers = newSubscribers
}

// Publish sends an event to all subscribers of a specific topic
func (eb *Topic[T]) Publish(event T) {
	eb.mutex.Lock()
	defer eb.mutex.Unlock()

	for _, subscriber := range eb.subscribers {
		subscriber <- event
	}
}
