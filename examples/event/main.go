package main

import (
	"time"

	"github.com/kofplayer/vactor"
)

var (
	TestActorType vactor.ActorType = vactor.ActorTypeStart + 1

	eventGroup1 vactor.EventGroup = "event_group1"
	eventId1    vactor.EventId    = 1
)

func main() {

	system := vactor.NewSystem()

	system.RegisterActorType(TestActorType, func() vactor.Actor {
		return func(ctx vactor.EnvelopeContext) {
			switch m := ctx.GetMessage().(type) {
			case *vactor.MsgOnStart:
				// inner event listen
				ctx.ListenEvent(eventGroup1, eventId1)

				// inner event fire
				ctx.FireEvent(eventGroup1, eventId1, 456)

			case *vactor.MsgOnEventMsg:
				ctx.LogDebug("on event %v", m.Message)
			}
		}
	})

	system.Start()

	// outer event listen
	queue := vactor.NewQueue[interface{}]()
	system.ListenEvent(eventGroup1, eventId1, queue)
	go func() {
		for {
			m, ok := queue.Dequeue()
			if !ok {
				break
			}
			system.LogDebug("outer on event %v", m.(*vactor.MsgOnEventMsg).Message)
		}
	}()

	system.Send(system.CreateActorRef(TestActorType, "1"), 1)

	time.Sleep(time.Second)

	// outer event fire
	system.FireEvent(eventGroup1, eventId1, 123)

	time.Sleep(time.Second)

	system.Stop()
}
