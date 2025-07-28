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
			switch ctx.GetMessage().(type) {
			case *vactor.MsgOnStart:
				ctx.SetSelfInvalid()
			}
		}
	})

	system.Start()

	_, err := system.Request(system.CreateActorRef(TestActorType, "1"), "hello", 0)
	if err != nil {
		system.LogError("request error: %v", err.Code())
	}

	time.Sleep(time.Second)

	system.Stop()
}
