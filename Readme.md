# vactor

vactor is a high-performance, lightweight virtual actor framework. It supports message sending, request/response, event dispatching, and watch/notify mechanisms. This is a single-process implementation that does not depend on any third-party libraries, making it easy to integrate into your project. It also provides support for distributed extensions; see my distributed implementation at [dvactor](https://github.com/kofplayer/dvactor). You can also implement your own distributed extension.

## Designd

- All actors always logically exist and cannot be actively created or destroyed.
- When sending a message to an actor, if the actor has not been created, the system will automatically create it.
- If an actor is inactive for a long time (configurable), the system will automatically destroy it.

## Features

- Actor type registration and creation
- Message sending and batch sending
- Synchronous and asynchronous request/response
- Event listening and dispatching
- Watch/Unwatch mechanism
- Efficient queue and ring buffer implementation

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
                ctx.LogDebug("Received message: %s", m)
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

See the [`examples`](examples) directory for more usage:

- [examples/hello/main.go](examples/hello/main.go): Basic message sending
- [examples/send/main.go](examples/send/main.go): Sending messages inside and outside actors
- [examples/request/main.go](examples/request/main.go): Request/response pattern
- [examples/event/main.go](examples/event/main.go): Event listening and dispatching
- [examples/watch/main.go](examples/watch/main.go): Watch/unwatch mechanism
- [examples/lifecycle/main.go](examples/lifecycle/main.go): Actor lifecycle

## License

Apache License 2.0. See [`LICENSE`](LICENSE) for details.