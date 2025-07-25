package vactor

type Envelope interface {
	GetToActorRef() ActorRef
}

type EnvelopeSend struct {
	FromActorRef ActorRef
	ToActorRef   ActorRef
	Message      interface{}
}

func (e *EnvelopeSend) GetToActorRef() ActorRef {
	return e.ToActorRef
}

type EnvelopeBatchSend struct {
	FromActorRef ActorRef
	ToActorRefs  []ActorRef
	Messages     []interface{}
}

func (e *EnvelopeBatchSend) GetToActorRef() ActorRef {
	return nil
}

type EnvelopeRequestAsync struct {
	FromActorRef    ActorRef
	ToActorRef      ActorRef
	Message         interface{}
	CallbackId      CallbackId
	CallbackAddress uint64
}

func (e *EnvelopeRequestAsync) GetToActorRef() ActorRef {
	return e.ToActorRef
}

type EnvelopeResponseAsync struct {
	*Response
	FromActorRef    ActorRef
	ToActorRef      ActorRef
	CallbackId      CallbackId
	CallbackAddress uint64
}

func (e *EnvelopeResponseAsync) GetToActorRef() ActorRef {
	return e.ToActorRef
}

type EnvelopeRequest struct {
	FromActorRef ActorRef
	ToActorRef   ActorRef
	Message      interface{}
}

func (e *EnvelopeRequest) GetToActorRef() ActorRef {
	return e.ToActorRef
}

type Response struct {
	Error   VAError
	Message interface{}
}

type EnvelopeResponse struct {
	*Response
	FromActorRef ActorRef
	ToActorRef   ActorRef
}

func (e *EnvelopeResponse) GetToActorRef() ActorRef {
	return e.ToActorRef
}

type EnvelopeOuterRequest struct {
	ToActorRef ActorRef
	Message    interface{}
	RspChan    chan *Response
}

func (e *EnvelopeOuterRequest) GetToActorRef() ActorRef {
	return e.ToActorRef
}

type EnvelopeWatch struct {
	FromActorRef ActorRef
	ToActorRef   ActorRef
	WatchType    WatchType
	IsWatch      bool
}

func (e *EnvelopeWatch) GetToActorRef() ActorRef {
	return e.ToActorRef
}

type NotifyType uint32

const (
	NotifyTypeWatch NotifyType = 0
	NotifyTypeEvent NotifyType = 1
)

type EnvelopeNotify struct {
	FromActorRef ActorRef
	ToActorRefs  []ActorRef
	NotifyType   NotifyType
	Message      *MsgOnWatchMsg
}

func (e *EnvelopeNotify) GetToActorRef() ActorRef {
	return nil
}

type EnvelopeFireNotify struct {
	FromActorRef ActorRef
	ToActorRef   ActorRef
	NotifyType   NotifyType
	WatchType    WatchType
	Message      interface{}
}

func (e *EnvelopeFireNotify) GetToActorRef() ActorRef {
	return e.ToActorRef
}

type EnvelopeOuterWatch struct {
	ToActorRef ActorRef
	WatchType  WatchType
	IsWatch    bool
	Queue      *Queue[interface{}]
}

func (e *EnvelopeOuterWatch) GetToActorRef() ActorRef {
	return e.ToActorRef
}

type envelopeStopedReport struct {
	fromActorRef ActorRef
}

func (e *envelopeStopedReport) GetToActorRef() ActorRef {
	return nil
}

type envelopeTick struct {
}

func (e *envelopeTick) GetToActorRef() ActorRef {
	return nil
}
