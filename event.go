package main

type EventType string

const (
	EventProgress EventType = "progress"
	EventLog      EventType = "log"
	EventError    EventType = "error"
)

type Event struct {
	Type     EventType
	Message  string
	Progress float64
}
