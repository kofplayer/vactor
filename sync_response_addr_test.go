package vactor_test

import (
	"testing"
	"time"

	"github.com/kofplayer/vactor"
	"github.com/kofplayer/vactor/testutil"
)

// 回归：携带"非法 CallbackAddress"（非 0 且不等于本实例 id）的同步响应必须被忽略，
// 不能当作正在等待的请求的结果。用于防止上一代（已回收重建）context 的陈旧响应串话。
func TestSyncResponseWithForeignCallbackAddressIgnored(t *testing.T) {
	fromSeen := make(chan vactor.ActorRef, 1)
	result := make(chan vactor.VAError, 1)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		registerEchoReq(s, 0, true, fromSeen) // echo 永不响应，只能靠注入
		s.RegisterActorType(reqCallerType, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				if _, ok := ctx.GetMessage().(string); !ok {
					return
				}
				_, err := ctx.Request(ctx.CreateActorRef(reqEchoType, "1"), "r1", 300*time.Millisecond)
				result <- err
			}
		})
	})

	callerRef := ts.CreateActorRef(reqCallerType, "c")
	ts.Send(callerRef, "go")
	// echo 收到请求说明 caller 已发出并进入等待（首个请求 requestId == 1）
	testutil.WaitChan(t, fromSeen, 3*time.Second, "echo received request")

	// 注入一个 requestId 匹配、但 CallbackAddress 指向"别的实例"的响应
	_ = ts.LocalRouter(&vactor.EnvelopeResponse{
		ToActorRef:      callerRef,
		RequestId:       1,
		CallbackAddress: ^uint64(0),
		Response:        &vactor.Response{Message: "fake"},
	})

	err := testutil.WaitChan(t, result, 3*time.Second, "caller resolved")
	if err == nil || err.Code() != vactor.ErrorCodeTimeout {
		t.Fatalf("foreign callbackAddress must be ignored, want timeout, got %v", err)
	}
}

// 回归：CallbackAddress 为 0（旧版本对端/手工构造的信封）必须按旧语义接受，
// 否则升级过渡期里跨节点同步请求会全部超时。
func TestSyncResponseLegacyZeroCallbackAddressAccepted(t *testing.T) {
	fromSeen := make(chan vactor.ActorRef, 1)
	result := make(chan string, 1)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		registerEchoReq(s, 0, true, fromSeen)
		s.RegisterActorType(reqCallerType, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				if _, ok := ctx.GetMessage().(string); !ok {
					return
				}
				rsp, err := ctx.Request(ctx.CreateActorRef(reqEchoType, "1"), "r1", 2*time.Second)
				if err != nil {
					result <- "ERR:" + err.Error()
					return
				}
				result <- rsp.(string)
			}
		})
	})

	callerRef := ts.CreateActorRef(reqCallerType, "c")
	ts.Send(callerRef, "go")
	testutil.WaitChan(t, fromSeen, 3*time.Second, "echo received request")

	_ = ts.LocalRouter(&vactor.EnvelopeResponse{
		ToActorRef:      callerRef,
		RequestId:       1,
		CallbackAddress: 0,
		Response:        &vactor.Response{Message: "legacy"},
	})

	if got := testutil.WaitChan(t, result, 3*time.Second, "caller resolved"); got != "legacy" {
		t.Fatalf("legacy zero callbackAddress should be accepted, got %q", got)
	}
}
