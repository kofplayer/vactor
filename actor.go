package vactor

type Actor func(EnvelopeContext)

type SystemId uint16
type GroupSlot uint16
type ActorType uint32
type ActorId string

type ActorRef interface {
	GetActorType() ActorType
	GetActorId() ActorId
	GetSystemId() SystemId
	GetGroupSlot() GroupSlot
}

type ActorRefImpl struct {
	SystemId  SystemId
	GroupSlot GroupSlot
	ActorType ActorType
	ActorId   ActorId
}

func (a *ActorRefImpl) GetActorType() ActorType {
	return a.ActorType
}
func (a *ActorRefImpl) GetActorId() ActorId {
	return a.ActorId
}
func (a *ActorRefImpl) GetSystemId() SystemId {
	return a.SystemId
}
func (a *ActorRefImpl) GetGroupSlot() GroupSlot {
	return a.GroupSlot
}

// type ActorRef struct {
// 	SystemId
// 	ActorType
// 	ActorId
// }

const (
	EventHubActorType ActorType = 1

	ActorTypeStart ActorType = 10
)

type CallbackId uint32

type WatchType uint32

type EventGroup string
type EventId uint32

type Router func(Envelope) error

type LogLevel int

const (
	DebugLevel LogLevel = iota
	InfoLevel
	WarnLevel
	ErrorLevel
	FatalLevel
	PanicLevel
)

func (l LogLevel) String() string {
	switch l {
	case DebugLevel:
		return "DEBUG"
	case InfoLevel:
		return "INFO"
	case WarnLevel:
		return "WARN"
	case ErrorLevel:
		return "ERROR"
	case FatalLevel:
		return "FATAL"
	case PanicLevel:
		return "PANIC"
	default:
		return "UNKNOWN"
	}
}

type LogFunc func(logLevel LogLevel, format string, args ...interface{})
type CreateActorRefExFunc func(systemId SystemId, actorType ActorType, actorId ActorId) ActorRef

type Logger interface {
	LogDebug(format string, args ...interface{})
	LogInfo(format string, args ...interface{})
	LogWarn(format string, args ...interface{})
	LogError(format string, args ...interface{})
	LogFatal(format string, args ...interface{})
	LogPanic(format string, args ...interface{})
}

type TimeoutError struct {
}

func (e *TimeoutError) Error() string {
	return "timeout"
}
