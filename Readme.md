[简体中文文档](ReadmeCh.md)

# vactor

vactor is a high-performance, lightweight virtual actor framework that does not depend on any third-party libraries.

## Design

- All actors always exist logically and cannot be explicitly created or destroyed.
- When sending a message to an actor, if the actor has not been created, the system will automatically create it.
- If an actor is inactive for a long time (configurable), the system will automatically destroy it.

## Features

- Actor type registration and creation
- Message sending and batch sending
- Synchronous and asynchronous request/response
- Event listening and dispatching
- Watch/Unwatch mechanism
- Efficient queue and ring buffer implementation

## Distributed Support

  The project itself does not directly support distributed systems, but provides support for distributed extensions. There is a distributed implementation available at [dvactor](https://github.com/kofplayer/dvactor). You can also implement your own distributed extension.

## Benchmark

[examples/benchmark/main.go](examples/benchmark/main.go) running on an i5-13400F processor (8 cores, 16 threads):
```sh
go run ./examples/benchmark
sent and processed 100000000 messages to 10000 actors in 4.7932951s
```

## Installation

```sh
go get github.com/kofplayer/vactor
```

## Quick Start

```go
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
                self := ctx.GetActorRef()
                ctx.LogDebug("actor %v-%v Received message: %s", self.GetActorType(), self.GetActorId(), m)
            }
        }
    })

    system.Start()
    system.Send(system.CreateActorRef(TestActorType, "1"), "hello actor")
    time.Sleep(time.Second)
    system.Stop()
}
```

## Examples

For more examples, see the [`examples`](examples) directory:

- [examples/hello/main.go](examples/hello/main.go): Basic message sending
- [examples/send/main.go](examples/send/main.go): Sending messages inside and outside actors
- [examples/request/main.go](examples/request/main.go): Request/response pattern
- [examples/event/main.go](examples/event/main.go): Event listening and dispatching
- [examples/watch/main.go](examples/watch/main.go): Watch/unwatch mechanism
- [examples/lifecycle/main.go](examples/lifecycle/main.go): Actor lifecycle

## License

MIT License. See [`LICENSE`](LICENSE) for details.