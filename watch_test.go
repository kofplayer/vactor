package vactor_test

import (
	"testing"
	"time"

	"github.com/kofplayer/vactor"
	"github.com/kofplayer/vactor/testutil"
)

const watchTestWT = vactor.WatchType(3)

// settle 等待 watch 关系建立：watcher 在其 OnStart 中异步发出 watch 信封，
// 需要留出送达时间后才能触发第一次 Notify。
func settle() { time.Sleep(200 * time.Millisecond) }

func TestInnerWatchNotifyFields(t *testing.T) {
	notify := make(chan *vactor.MsgOnWatchMsg, 8)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, func() vactor.Actor { // watcher
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case *vactor.MsgOnStart:
					ctx.Watch(ctx.CreateActorRef(101, "t"), watchTestWT)
				case *vactor.MsgOnWatchMsg:
					notify <- ctx.GetMessage().(*vactor.MsgOnWatchMsg)
				}
			}
		})
		s.RegisterActorType(101, func() vactor.Actor { // watchee
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case string:
					ctx.Notify(watchTestWT, "payload")
				}
			}
		})
	})
	ts.Send(ts.CreateActorRef(100, "w"), "boot")
	settle()
	ts.Send(ts.CreateActorRef(101, "t"), "go")

	m := testutil.WaitChan(t, notify, 3*time.Second, "watch notify")
	if m.WatchType != watchTestWT {
		t.Fatalf("watchType = %v", m.WatchType)
	}
	if m.ActorRef.GetActorId() != "t" || m.ActorRef.GetActorType() != 101 {
		t.Fatalf("ActorRef = %v/%v", m.ActorRef.GetActorType(), m.ActorRef.GetActorId())
	}
	if m.Message != "payload" {
		t.Fatalf("message = %v", m.Message)
	}
}

func TestInnerUnwatchStopsDelivery(t *testing.T) {
	notify := make(chan *vactor.MsgOnWatchMsg, 8)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, func() vactor.Actor { // watcher
			return func(ctx vactor.EnvelopeContext) {
				switch m := ctx.GetMessage().(type) {
				case *vactor.MsgOnStart:
					ctx.Watch(ctx.CreateActorRef(101, "t"), watchTestWT)
				case *vactor.MsgOnWatchMsg:
					notify <- m
				case string:
					if m == "unwatch" {
						ctx.Unwatch(ctx.CreateActorRef(101, "t"), watchTestWT)
					}
				}
			}
		})
		s.RegisterActorType(101, func() vactor.Actor { // watchee
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case string:
					ctx.Notify(watchTestWT, "payload")
				}
			}
		})
	})
	ts.Send(ts.CreateActorRef(100, "w"), "boot")
	settle()
	ts.Send(ts.CreateActorRef(101, "t"), "go")
	testutil.WaitChan(t, notify, 3*time.Second, "first notify")

	ts.Send(ts.CreateActorRef(100, "w"), "unwatch")
	time.Sleep(100 * time.Millisecond) // 等 unwatch 信封送达 watchee
	ts.Send(ts.CreateActorRef(101, "t"), "go")
	testutil.NoReceive(t, notify, 500*time.Millisecond, "notify after unwatch")
}

func TestMultipleWatchersAllNotified(t *testing.T) {
	n1 := make(chan *vactor.MsgOnWatchMsg, 8)
	n2 := make(chan *vactor.MsgOnWatchMsg, 8)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, func() vactor.Actor { // 两个 watcher 同类型不同 id
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case *vactor.MsgOnStart:
					ctx.Watch(ctx.CreateActorRef(101, "t"), watchTestWT)
				case *vactor.MsgOnWatchMsg:
					ch := n1
					if ctx.GetActorRef().GetActorId() == "w2" {
						ch = n2
					}
					ch <- ctx.GetMessage().(*vactor.MsgOnWatchMsg)
				}
			}
		})
		s.RegisterActorType(101, func() vactor.Actor { // watchee
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case string:
					ctx.Notify(watchTestWT, "payload")
				}
			}
		})
	})
	ts.Send(ts.CreateActorRef(100, "w1"), "boot")
	ts.Send(ts.CreateActorRef(100, "w2"), "boot")
	settle()
	ts.Send(ts.CreateActorRef(101, "t"), "go")
	testutil.WaitChan(t, n1, 3*time.Second, "watcher w1 notified")
	testutil.WaitChan(t, n2, 3*time.Second, "watcher w2 notified")
}

func TestWatchTypesIndependent(t *testing.T) {
	seen3 := make(chan *vactor.MsgOnWatchMsg, 8)
	seen4 := make(chan *vactor.MsgOnWatchMsg, 8)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, func() vactor.Actor { // watcher
			return func(ctx vactor.EnvelopeContext) {
				switch m := ctx.GetMessage().(type) {
				case *vactor.MsgOnStart:
					ctx.Watch(ctx.CreateActorRef(101, "t"), 3)
					ctx.Watch(ctx.CreateActorRef(101, "t"), 4)
				case *vactor.MsgOnWatchMsg:
					if m.WatchType == 3 {
						seen3 <- m
					} else if m.WatchType == 4 {
						seen4 <- m
					}
				}
			}
		})
		s.RegisterActorType(101, func() vactor.Actor { // watchee
			return func(ctx vactor.EnvelopeContext) {
				switch m := ctx.GetMessage().(type) {
				case string:
					if m == "fire3" {
						ctx.Notify(3, "p3")
					} else if m == "fire4" {
						ctx.Notify(4, "p4")
					}
				}
			}
		})
	})
	ts.Send(ts.CreateActorRef(100, "w"), "boot")
	settle()
	ts.Send(ts.CreateActorRef(101, "t"), "fire3")
	m3 := testutil.WaitChan(t, seen3, 3*time.Second, "type 3 notify")
	testutil.NoReceive(t, seen4, 200*time.Millisecond, "type 4 should not fire")
	ts.Send(ts.CreateActorRef(101, "t"), "fire4")
	m4 := testutil.WaitChan(t, seen4, 3*time.Second, "type 4 notify")
	if m3.WatchType != 3 || m4.WatchType != 4 {
		t.Fatal("watch type mismatch")
	}
}

func TestOuterWatchQueue(t *testing.T) {
	queue := vactor.NewQueue[interface{}]()
	seen := make(chan *vactor.MsgOnWatchMsg, 8)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(101, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case string:
					ctx.Notify(watchTestWT, "payload")
				}
			}
		})
	})
	ts.Watch(ts.CreateActorRef(101, "t"), watchTestWT, queue)
	go func() {
		for {
			m, ok := queue.Dequeue()
			if !ok {
				return
			}
			if msg, ok := m.(*vactor.MsgOnWatchMsg); ok {
				seen <- msg
			}
		}
	}()

	ts.Send(ts.CreateActorRef(101, "t"), "go")
	m := testutil.WaitChan(t, seen, 3*time.Second, "outer watch notify")
	if m.Message != "payload" || m.ActorRef.GetActorId() != "t" {
		t.Fatalf("outer notify fields wrong: %+v", m)
	}

	// Unwatch 后停止投递
	ts.Unwatch(ts.CreateActorRef(101, "t"), watchTestWT, queue)
	time.Sleep(100 * time.Millisecond)
	ts.Send(ts.CreateActorRef(101, "t"), "go")
	testutil.NoReceive(t, seen, 500*time.Millisecond, "notify after unwatch")
}

// 关闭外部 Queue 会自动退订：不再投递，且系统继续正常工作。
func TestOuterWatchClosedQueueAutoUnsubscribes(t *testing.T) {
	queue := vactor.NewQueue[interface{}]()
	col := &testutil.Collector{}
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(101, col.Creator())
		s.RegisterActorType(100, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case string:
					ctx.Notify(watchTestWT, "payload")
				}
			}
		})
	})
	ts.Watch(ts.CreateActorRef(100, "w"), watchTestWT, queue)
	queue.Close()

	// 向已关闭队列投递不应 panic，且自动移除该 watcher
	ts.Send(ts.CreateActorRef(100, "w"), "go")
	time.Sleep(200 * time.Millisecond)

	// 系统仍然健康：业务消息正常处理
	ts.Send(ts.CreateActorRef(101, "c"), "hello")
	col.WaitForMessages(t, 1, 2*time.Second, "system healthy after closed queue")
}
