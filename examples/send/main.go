package main

import (
	"time"

	"github.com/kofplayer/vactor"
)

func main() {
	TestActorType := vactor.ActorTypeStart + 1
	system := vactor.NewSystem()
	system.RegisterActorType(TestActorType, func() vactor.Actor {
		return func(ctx vactor.EnvelopeContext) {
			switch m := ctx.GetMessage().(type) {
			case *vactor.MsgOnStart:
				// inner send
				ctx.Send(ctx.GetActorRef(), "hello from self")
			case string:
				ctx.LogDebug("Received message: %s", m)
			}
		}
	})
	system.Start()

	// outer send
	system.Send(system.CreateActorRef(TestActorType, "1"), "hello from outer")

	time.Sleep(time.Second)

	system.Stop()
}
