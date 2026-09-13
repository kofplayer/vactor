package vactor_test

import (
	"testing"
	"time"

	"github.com/kofplayer/vactor"
	"github.com/kofplayer/vactor/testutil"
)

// 覆盖 actor 内上下文 API 的转发路径：CreateActorRefEx / BatchSend / LocalRouter /
// FireEvent，以及 ctx 的 5 个日志方法（必须经 SystemConfig.LogFunc 输出）。
func TestContextAPIForwarding(t *testing.T) {
	const (
		apiCallerType vactor.ActorType = 150
		apiTargetType vactor.ActorType = 151
	)
	col := &testutil.Collector{}
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(apiCallerType, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				m, ok := ctx.GetMessage().(string)
				if !ok {
					return
				}
				switch m {
				case "logs":
					ctx.LogDebug("dbg")
					ctx.LogInfo("inf")
					ctx.LogWarn("wrn")
					ctx.LogError("err")
					ctx.LogFatal("ftl")
				case "api":
					ref := ctx.CreateActorRefEx(0, apiTargetType, "t")
					// BatchSend 是笛卡尔广播：目标 ref 会依次收到全部 messages
					_ = ctx.BatchSend([]vactor.ActorRef{ref}, []interface{}{"b1", "b2"})
					ctx.LocalRouter(&vactor.EnvelopeSend{
						FromActorRef: ctx.GetActorRef(),
						ToActorRef:   ref,
						Message:      "lr",
					})
					// 无监听者的事件必须静默完成
					ctx.FireEvent("grp", vactor.EventId(7), "evt")
				}
			}
		})
		s.RegisterActorType(apiTargetType, col.Creator())
	})

	ts.Send(ts.CreateActorRef(apiCallerType, "c"), "logs")
	testutil.WaitFor(t, 2*time.Second, "ctx log methods emit", func() bool {
		return ts.LogContains("DEBUG dbg") && ts.LogContains("INFO inf") &&
			ts.LogContains("WARN wrn") && ts.LogContains("ERROR err") && ts.LogContains("FATAL ftl")
	})

	ts.Send(ts.CreateActorRef(apiCallerType, "c"), "api")
	msgs := col.WaitForMessages(t, 3, 3*time.Second, "target receives batch + local router messages")
	got := map[string]bool{}
	for _, m := range msgs {
		if s, ok := m.(string); ok {
			got[s] = true
		}
	}
	for _, want := range []string{"b1", "b2", "lr"} {
		if !got[want] {
			t.Fatalf("target missing %q, got %v", want, msgs)
		}
	}
	if !ts.IsRunning() {
		t.Fatal("system should stay running after FireEvent with no listener")
	}
}

// actor 内引用自身的 actorType/id/systemId 查询路径。
func TestContextRefAccessors(t *testing.T) {
	const refType vactor.ActorType = 152
	self := make(chan vactor.ActorRef, 1)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(refType, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				if _, ok := ctx.GetMessage().(string); ok {
					self <- ctx.GetActorRef()
				}
			}
		})
	})
	ref := ts.CreateActorRef(refType, "self")
	ts.Send(ref, "go")
	got := testutil.WaitChan(t, self, 2*time.Second, "actor ref reported")
	if got.GetActorType() != refType || got.GetActorId() != "self" || got.GetSystemId() != ref.GetSystemId() {
		t.Fatalf("actor ref mismatch: %+v", got)
	}
}
