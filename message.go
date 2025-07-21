package vactor

type MsgOnStart struct{}

type MsgOnStop struct{}

type MsgOnTick struct{}

type MsgOnWatchMsg struct {
	ActorRef
	WatchType
	Message interface{}
}

type MsgOnEventMsg struct {
	EventGroup
	EventId
	Message interface{}
}
