package main

import (
	"fmt"
	"time"

	"github.com/kofplayer/vactor"
)

func main() {
	wt := vactor.WatchType(111)
	WatcherType := vactor.ActorTypeStart + 1
	WatcheeType := vactor.ActorTypeStart + 2

	system := vactor.NewSystem()

	system.RegisterActorType(WatcherType, func() vactor.Actor {
		return func(ctx vactor.EnvelopeContext) {
			switch m := ctx.GetMessage().(type) {
			case *vactor.MsgOnStart:
				// inner watch
				ctx.Watch(system.CreateActorRef(WatcheeType, "1"), wt)
			case *vactor.MsgOnWatchMsg:
				fmt.Printf("inner watch: %v\n", m.Message)

				// inner unwatch
				ctx.Unwatch(system.CreateActorRef(WatcheeType, "1"), wt)
			}
		}
	})

	system.RegisterActorType(WatcheeType, func() vactor.Actor {
		return func(ctx vactor.EnvelopeContext) {
			switch ctx.GetMessage().(type) {
			case *vactor.MsgOnTick:
				// notify watcher
				ctx.Notify(wt, "notfiy")
			}
		}
	})

	system.Start()

	// outer watch
	queue := vactor.NewQueue[interface{}]()
	system.Watch(system.CreateActorRef(WatcheeType, "1"), wt, queue)
	go func() {
		for {
			m, ok := queue.Dequeue()
			if !ok {
				return
			}
			switch t := m.(*vactor.MsgOnWatchMsg).Message.(type) {
			case string:
				fmt.Printf("outer watch: %v\n", t)
			}

			// outer unwatch
			system.Unwatch(system.CreateActorRef(WatcheeType, "1"), wt, queue)
			//queue.Close()
		}
	}()

	system.Send(system.CreateActorRef(WatcherType, "1"), "hello")

	time.Sleep(time.Second * 2)

	system.Stop()
}
