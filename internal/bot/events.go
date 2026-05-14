package bot

// Event is any normalized platform event. Concrete types in this file
// implement the marker method isEvent.
//
// Consumers do a type switch:
//
//	for ev := range provider.Events() {
//	    switch e := ev.(type) {
//	    case bot.EventMessageNew: ...
//	    case bot.EventConnected:  ...
//	    }
//	}
type Event interface {
	isEvent()
}

// EventConnected is emitted once after a successful Connect, carrying
// the resolved Identity. Useful for consumers that want to know the
// bot's user id without a separate roundtrip.
type EventConnected struct {
	Identity Identity
}

func (EventConnected) isEvent() {}

// EventDisconnected is emitted when the backend connection drops or
// when Disconnect is called. Reason is best-effort human-readable
// context (no parsing).
type EventDisconnected struct {
	Reason string
}

func (EventDisconnected) isEvent() {}

// EventMessageNew is emitted when a new message arrives in any
// channel the bot can observe.
type EventMessageNew struct {
	Message Message
}

func (EventMessageNew) isEvent() {}
