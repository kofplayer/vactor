package vactor_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/kofplayer/vactor"
	"github.com/kofplayer/vactor/testutil"
)

const (
	eventTestGroup = vactor.EventGroup("orders")
	eventTestID    = vactor.EventId(9)
)

// 在系统上注册一个事件监听 actor：启动时 ListenEvent，
// 收到事件后写入 out 通道；收到 "unlisten" 消息后取消订阅。
func registerEventListener(s vactor.System, actorType vactor.ActorType, out chan *vactor.MsgOnEventMsg) {
	s.RegisterActorType(actorType, func() vactor.Actor {
		return func(ctx vactor.EnvelopeContext) {
			switch m := ctx.GetMessage().(type) {
			case *vactor.MsgOnStart:
				ctx.ListenEvent(eventTestGroup, eventTestID)
			case *vactor.MsgOnEventMsg:
				out <- m
			case string:
				if m == "unlisten" {
					ctx.UnlistenEvent(eventTestGroup, eventTestID)
				}
			}
		}
	})
}

func TestFireEventFields(t *testing.T) {
	events := make(chan *vactor.MsgOnEventMsg, 8)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		registerEventListener(s, 100, events)
	})
	ts.Send(ts.CreateActorRef(100, "l"), "boot")
	time.Sleep(200 * time.Millisecond) // 等 ListenEvent 信封送达 EventHub

	ts.FireEvent(eventTestGroup, eventTestID, "payload-1")
	m := testutil.WaitChan(t, events, 3*time.Second, "event delivered")
	if m.EventGroup != eventTestGroup || m.EventId != eventTestID {
		t.Fatalf("event fields wrong: group=%v id=%v", m.EventGroup, m.EventId)
	}
	if m.Message != "payload-1" {
		t.Fatalf("event message = %v", m.Message)
	}
}

func TestEventOrderingWithinGroup(t *testing.T) {
	events := make(chan *vactor.MsgOnEventMsg, 64)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		registerEventListener(s, 100, events)
	})
	ts.Send(ts.CreateActorRef(100, "l"), "boot")
	time.Sleep(200 * time.Millisecond)

	const n = 30
	for i := 0; i < n; i++ {
		ts.FireEvent(eventTestGroup, eventTestID, strconv.Itoa(i))
	}
	var got []int
	for len(got) < n {
		m := testutil.WaitChan(t, events, 3*time.Second, "event in order")
		v, err := strconv.Atoi(m.Message.(string))
		if err != nil {
			t.Fatalf("bad payload %v", m.Message)
		}
		got = append(got, v)
	}
	for i, v := range got {
		if v != i {
			t.Fatalf("event order broken: got %v", got)
		}
	}
}

func TestUnlistenEventStopsDelivery(t *testing.T) {
	events := make(chan *vactor.MsgOnEventMsg, 8)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		registerEventListener(s, 100, events)
	})
	ts.Send(ts.CreateActorRef(100, "l"), "boot")
	time.Sleep(200 * time.Millisecond)

	ts.FireEvent(eventTestGroup, eventTestID, "before")
	testutil.WaitChan(t, events, 3*time.Second, "event before unlisten")

	ts.Send(ts.CreateActorRef(100, "l"), "unlisten")
	time.Sleep(200 * time.Millisecond)
	ts.FireEvent(eventTestGroup, eventTestID, "after")
	testutil.NoReceive(t, events, 500*time.Millisecond, "event after unlisten")
}

// EventHub actor 闲置回收后，监听关系通过 cache 保留。
func TestEventHubRecyclePreservesListeners(t *testing.T) {
	events := make(chan *vactor.MsgOnEventMsg, 8)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		registerEventListener(s, 100, events)
	}, testutil.WithStopInterval(200*time.Millisecond))
	ts.Send(ts.CreateActorRef(100, "l"), "boot")
	time.Sleep(200 * time.Millisecond)

	ts.FireEvent(eventTestGroup, eventTestID, "before-recycle")
	testutil.WaitChan(t, events, 3*time.Second, "event before hub recycle")

	time.Sleep(700 * time.Millisecond) // 让 EventHub 闲置回收

	ts.FireEvent(eventTestGroup, eventTestID, "after-recycle")
	m := testutil.WaitChan(t, events, 3*time.Second, "event after hub recycle (listener cache preserved)")
	if m.Message != "after-recycle" {
		t.Fatalf("message = %v", m.Message)
	}
}

func TestFireEventNoListenerNoPanic(t *testing.T) {
	ts := testutil.NewSystem(t, nil)
	ts.FireEvent(eventTestGroup, eventTestID, "nobody-listens")
	time.Sleep(200 * time.Millisecond)
	// 系统仍然健康：再发一次也不 panic
	ts.FireEvent(eventTestGroup, eventTestID, "still-ok")
}

func TestDifferentEventGroupsIndependent(t *testing.T) {
	g1 := make(chan *vactor.MsgOnEventMsg, 8)
	g2 := make(chan *vactor.MsgOnEventMsg, 8)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch m := ctx.GetMessage().(type) {
				case *vactor.MsgOnStart:
					ctx.ListenEvent("g1", 1)
				case *vactor.MsgOnEventMsg:
					g1 <- m
				}
			}
		})
		s.RegisterActorType(101, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch m := ctx.GetMessage().(type) {
				case *vactor.MsgOnStart:
					ctx.ListenEvent("g2", 1)
				case *vactor.MsgOnEventMsg:
					g2 <- m
				}
			}
		})
	})
	ts.Send(ts.CreateActorRef(100, "l1"), "boot")
	ts.Send(ts.CreateActorRef(101, "l2"), "boot")
	time.Sleep(200 * time.Millisecond)

	ts.FireEvent("g1", 1, "a1")
	ts.FireEvent("g2", 1, "b1")
	ts.FireEvent("g1", 1, "a2")
	ts.FireEvent("g2", 1, "b2")

	if m := testutil.WaitChan(t, g1, 3*time.Second, "g1 event 1"); m.Message != "a1" {
		t.Fatalf("g1 first = %v", m.Message)
	}
	if m := testutil.WaitChan(t, g1, 3*time.Second, "g1 event 2"); m.Message != "a2" {
		t.Fatalf("g1 second = %v", m.Message)
	}
	if m := testutil.WaitChan(t, g2, 3*time.Second, "g2 event 1"); m.Message != "b1" {
		t.Fatalf("g2 first = %v", m.Message)
	}
	if m := testutil.WaitChan(t, g2, 3*time.Second, "g2 event 2"); m.Message != "b2" {
		t.Fatalf("g2 second = %v", m.Message)
	}
}

// 系统级 ListenEvent（外部 Queue 订阅）收到 *MsgOnEventMsg，UnlistenEvent 停止投递。
func TestSystemLevelListenEvent(t *testing.T) {
	ts := testutil.NewSystem(t, nil)
	queue := vactor.NewQueue[interface{}]()
	seen := make(chan *vactor.MsgOnEventMsg, 8)
	ts.ListenEvent(eventTestGroup, eventTestID, queue)
	go func() {
		for {
			m, ok := queue.Dequeue()
			if !ok {
				return
			}
			if msg, ok := m.(*vactor.MsgOnEventMsg); ok {
				seen <- msg
			}
		}
	}()

	ts.FireEvent(eventTestGroup, eventTestID, "outer")
	m := testutil.WaitChan(t, seen, 3*time.Second, "system-level event")
	if m.EventGroup != eventTestGroup || m.Message != "outer" {
		t.Fatalf("fields wrong: %+v", m)
	}

	ts.UnlistenEvent(eventTestGroup, eventTestID, queue)
	time.Sleep(200 * time.Millisecond)
	ts.FireEvent(eventTestGroup, eventTestID, "after-unlisten")
	testutil.NoReceive(t, seen, 500*time.Millisecond, "event after unlisten")
}
