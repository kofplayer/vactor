package main

import (
	"time"

	"github.com/kofplayer/vactor"
)

func main() {

	RequesterType := vactor.ActorTypeStart + 1
	ResponserType := vactor.ActorTypeStart + 2

	system := vactor.NewSystem()
	system.RegisterActorType(ResponserType, func() vactor.Actor {
		return func(ctx vactor.EnvelopeContext) {
			switch m := ctx.GetMessage().(type) {
			case string:
				ctx.Response("rsponse for "+m, nil)
			}
		}
	})

	responser := system.CreateActorRef(ResponserType, "1")

	system.RegisterActorType(RequesterType, func() vactor.Actor {
		return func(ctx vactor.EnvelopeContext) {
			switch ctx.GetMessage().(type) {
			case *vactor.MsgOnStart:
				// inner sync request
				ctx.RequestAsync(responser, "req async", 0, func(msg interface{}, err vactor.VAError) {
					switch rsp := msg.(type) {
					case string:
						ctx.LogDebug("async receive %#v\n", rsp)
					default:
						ctx.LogDebug("async Unexpected response type: %T\n", rsp)
					}
				})

				// inner async request
				msg, _ := ctx.Request(responser, "req sync", 0)
				switch rsp := msg.(type) {
				case string:
					ctx.LogDebug("sync receive %#v\n", rsp)
				default:
					ctx.LogDebug("sync Unexpected response type: %T\n", rsp)
				}
			}
		}
	})
	system.Start()

	system.Send(system.CreateActorRef(RequesterType, "1"), "hello")

	// outer request
	rsp, err := system.Request(responser, "req outer", 0)
	if err != nil {
		system.LogDebug("request error: %v\n", err)
	} else {
		system.LogDebug("request response: %v\n", rsp)
	}

	time.Sleep(time.Second)

	system.Stop()
}
