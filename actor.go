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

const (
	EventHubActorType ActorType = 1

	ActorTypeStart ActorType = 10
)

// CallbackId 同时用作异步回调标识与同步请求/响应序号。
// 取 uint64：单调递增的序号在 32 位下会于 40 亿次请求后回绕，
// 长跑进程可能因此把新回调与残留条目混淆。
type CallbackId uint64

type WatchType uint32

type EventGroup string
type EventId uint32

type Router func(Envelope) VAError

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

// HashActorId 计算 ActorId 的 32 位 FNV-1a 哈希。
//
// 单机分片与集群放置**共用**这一个函数——若两处各用一套哈希，同一 actor 在
// vactor 与 dvactor 下会得到互不一致的落点。
//   - vactor：取低 16 位作为 GroupSlot（落组公式 (GroupSlot-1)%groupCount）
//   - dvactor：用 hash%节点数 选放置节点，再用商做分片
//
// 早先的实现是"从尾部向前、按位置交替 XOR 进 2/4 个字节桶"的弱哈希。实测
// （2 万个结构化 id、16 个 group）它把 user1..user20000 压进仅 762 个槽位，
// 最重桶达最轻桶的 2.5 倍（变异系数 0.37）；结构化 id（user123、
// room-1-player-2）恰是业务常态。换成 FNV-1a 后唯一槽位 17586、变异系数 0.006。
func HashActorId(actorId ActorId) uint32 {
	const (
		offset32 = uint32(2166136261)
		prime32  = uint32(16777619)
	)
	h := offset32
	for i := 0; i < len(actorId); i++ {
		h ^= uint32(actorId[i])
		h *= prime32
	}
	return h
}
