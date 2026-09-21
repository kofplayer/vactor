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

	// 用有限超时：timeout<=0 是无限等待，示例不应该演示会挂死的调用方式
	rsp, err := system.Request(system.CreateActorRef(TestActorType, "1"), "hello", time.Second)
	_ = rsp
	if err != nil {
		system.LogError("request error: %v", err.Code())
	}

	time.Sleep(time.Second)

	system.Stop()
}
