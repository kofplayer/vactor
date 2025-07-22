# vactor

vactor 是一个高性能、轻量级的 虚拟actor 框架，不依赖任何第三方库。

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

## 分布式支持

  项目本身不直接支持分布式，但是给分布式扩展提供了支持。我这里有一个分布式的实现 [dvactor](https://github.com/kofplayer/dvactor) 。你也可以实现自己的分布式扩展。

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

## 示例

更多示例请参考 [`examples`](examples) 目录：

- [examples/hello/main.go](examples/hello/main.go)：基础消息发送
- [examples/send/main.go](examples/send/main.go)：Actor 内外消息发送
- [examples/request/main.go](examples/request/main.go)：请求/响应模
- [examples/event/main.go](examples/event/main.go)：事件监听与分发
- [examples/watch/main.go](examples/watch/main.go)：watch/unwatch 机制
- [examples/liftcycle/main.go](examples/lifecycle/main.go)：actor 生命周期

## License

MIT License. 详情见 [`LICENSE`](LICENSE)。