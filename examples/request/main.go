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
				ctx.Response("response for "+m, nil)
			}
		}
	})

	responser := system.CreateActorRef(ResponserType, "1")

	system.RegisterActorType(RequesterType, func() vactor.Actor {
		return func(ctx vactor.EnvelopeContext) {
			switch ctx.GetMessage().(type) {
			case *vactor.MsgOnStart:
				// inner async request
				// 三处 timeout 一律给有限值：timeout<=0 是无限等待，目标不响应时
				// 同步版会永久挂死本 actor、异步版的回调则永久悬挂（见 docs/api-reference.md）
				ctx.RequestAsync(responser, "req async", time.Second, func(msg interface{}, err vactor.VAError) {
					if err != nil {
						ctx.LogDebug("async error: %v", err)
						return
					}
					switch rsp := msg.(type) {
					case string:
						ctx.LogDebug("async receive %#v\n", rsp)
					default:
						ctx.LogDebug("async Unexpected response type: %T\n", rsp)
					}
				})

				// inner sync request：阻塞本 actor goroutine，务必带超时
				msg, err := ctx.Request(responser, "req sync", time.Second)
				if err != nil {
					ctx.LogDebug("sync error: %v", err)
					return
				}
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
	rsp, err := system.Request(responser, "req outer", time.Second)
	if err != nil {
		system.LogDebug("request error: %v\n", err)
	} else {
		system.LogDebug("request response: %v\n", rsp)
	}

	time.Sleep(time.Second)

	system.Stop()
}
