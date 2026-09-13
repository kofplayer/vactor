// 大吞吐人工观察版压测：1 万 actor × 1 万消息，在固定机器上看端到端量级。
//
// 需要能被 CI 驱动、可与不同实现对比的标准基准在仓库根的 bench_test.go：
//
//	go test -run '^$' -bench . -benchmem ./
package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/kofplayer/vactor"
)

const (
	ActorTypeBench = vactor.ActorTypeStart + 100
	ActorCount     = 10000
	MsgPerActor    = 10000
)

func main() {
	system := vactor.NewSystem()

	var wg sync.WaitGroup
	wg.Add(ActorCount * MsgPerActor)
	system.RegisterActorType(ActorTypeBench, func() vactor.Actor {
		return func(ctx vactor.EnvelopeContext) {
			switch ctx.GetMessage().(type) {
			case string:
				wg.Done()
			}
		}
	})

	system.Start()
	start := time.Now()

	for i := 0; i < ActorCount; i++ {
		ref := system.CreateActorRef(ActorTypeBench, vactor.ActorId(fmt.Sprintf("%d", i)))
		go func(ref vactor.ActorRef) {
			for j := 0; j < MsgPerActor; j++ {
				system.Send(ref, "ping")
			}
		}(ref)
	}

	wg.Wait()
	elapsed := time.Since(start)
	fmt.Printf("sent and processed %d messages to %d actors in %v\n", ActorCount*MsgPerActor, ActorCount, elapsed)

	system.Stop()
}
