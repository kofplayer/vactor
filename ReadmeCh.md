# vactor

vactor 是一个高性能、轻量级的 虚拟actor 框架，支持消息发送、请求响应、事件分发、watch/notify 等功能。这是一个不依赖任何第三方库的单进程的实现，可以很容易集成到你的项目中。同时也给分布式扩展提供了支持，我这里有一个分布式的实现 [dvactor](https://github.com/kofplayer/dvactor) 。你也可以实现自己的分布式扩展。

## 设计

- 所有actor总是逻辑上存在的，不能主动创建和销毁。
- 当给actor发送消息时，如果actor还没有创建，系统会自动创建。
- actor长时间不活跃(时间可以设置)，系统会自动销毁actor。

## 特性

- Actor 类型注册与创建
- 消息发送与批量发送
- 请求/响应（同步与异步）
- 事件监听与分发
- Watch/Unwatch 机制
- 高效队列与 ring buffer 实现

## 安装

```sh
go get github.com/kofplayer/vactor
```

## 快速开始

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

## 示例

更多示例请参考 [`examples`](examples) 目录：

- [examples/hello/main.go](examples/hello/main.go)：基础消息发送
- [examples/send/main.go](examples/send/main.go)：Actor 内外消息发送
- [examples/request/main.go](examples/request/main.go)：请求/响应模
- [examples/event/main.go](examples/event/main.go)：事件监听与分发
- [examples/watch/main.go](examples/watch/main.go)：watch/unwatch 机制
- [examples/liftcycle/main.go](examples/lifecycle/main.go)：actor 生命周期

## License

Apache License 2.0. 详情见 [`LICENSE`](LICENSE)。