package domain

import "time"

// EventType identifies the kind of domain event.
type EventType string

const (
	EventCreated EventType = "created"
	EventUpdated EventType = "updated"
	EventMoved   EventType = "moved"
	EventDeleted EventType = "deleted"
	EventScanned EventType = "scanned"
)

// Event represents a domain event emitted when the filesystem state changes.
type Event struct {
	Type EventType
	ID   FileID
	Path string
	At   time.Time
}

// EventBus is the port interface for publishing and subscribing to domain events.
type EventBus interface {
	Publish(event Event)
	Subscribe(eventType EventType, handler func(Event))
}
