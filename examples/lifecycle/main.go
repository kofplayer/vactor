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
				ctx.LogDebug("Actor started: %#v", ctx.GetActorRef())

				// actor will stop after 5 seconds if no messages are received (excluding MsgOnTick)
				ctx.SetStopInterval(5 * time.Second)
			case *vactor.MsgOnStop:
				ctx.LogDebug("Actor stopped: %#v", ctx.GetActorRef())
			case *vactor.MsgOnTick:
				ctx.LogDebug("Tick received in actor: %#v", ctx.GetActorRef())
			case string:
				ctx.LogDebug("Received message: %s", m)
			}
		}
	})

	system.Start()

	system.Send(system.CreateActorRef(TestActorType, "1"), "hello")

	time.Sleep(time.Second * 6)

	system.Stop()
}
