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
			case string:
				ctx.LogDebug("Received message: %s", m)
			}
		}
	})

	system.Start()

	system.Send(system.CreateActorRef(TestActorType, "1"), "hello")

	time.Sleep(time.Second)

	system.Stop()
}
