package vactor

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 注册一个 echo actor：收到 string 后延迟 delay 再响应
func registerEchoActor(system System, actorType ActorType, delay time.Duration) {
	system.RegisterActorType(actorType, func() Actor {
		return func(ctx EnvelopeContext) {
			switch m := ctx.GetMessage().(type) {
			case string:
				if delay > 0 {
					time.Sleep(delay)
				}
				ctx.Response("echo:"+m, nil)
			}
		}
	})
}

func TestSyncRequestResponse(t *testing.T) {
	system := NewSystem()
	echoType := ActorTypeStart + 1
	registerEchoActor(system, echoType, 0)
	system.Start()
	defer system.Stop()

	rsp, err := system.Request(system.CreateActorRef(echoType, "1"), "hello", time.Second)
	if err != nil {
		t.Fatalf("request error: %v", err.Code())
	}
	if rsp != "echo:hello" {
		t.Fatalf("unexpected response: %v", rsp)
	}
}

func TestSyncRequestTimeout(t *testing.T) {
	system := NewSystem()
	echoType := ActorTypeStart + 1
	registerEchoActor(system, echoType, 300*time.Millisecond)
	system.Start()
	defer system.Stop()

	_, err := system.Request(system.CreateActorRef(echoType, "1"), "hello", 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if err.Code() != ErrorCodeTimeout {
		t.Fatalf("expected ErrorCodeTimeout, got %v", err.Code())
	}
}

// 回归测试：同步 Request 超时后，迟到的旧响应不得被下一个无关 Request 消费
func TestStaleSyncResponseDropped(t *testing.T) {
	system := NewSystem()
	echoType := ActorTypeStart + 1
	callerType := ActorTypeStart + 2
	registerEchoActor(system, echoType, 300*time.Millisecond)

	done := make(chan interface{}, 1)
	system.RegisterActorType(callerType, func() Actor {
		return func(ctx EnvelopeContext) {
			switch ctx.GetMessage().(type) {
			case *MsgOnStart:
				echoRef := ctx.CreateActorRef(echoType, "1")
				// 第一次请求：50ms 超时（echo 300ms 才回）
				_, err := ctx.Request(echoRef, "first", 50*time.Millisecond)
				if err == nil || err.Code() != ErrorCodeTimeout {
					done <- "first request should timeout"
					return
				}
				time.Sleep(100 * time.Millisecond)
				// 第二次请求：旧响应（first）此时到达，必须被丢弃
				rsp, err := ctx.Request(echoRef, "second", 3*time.Second)
				if err != nil {
					done <- "second request error: " + err.Error()
					return
				}
				done <- rsp
			}
		}
	})
	system.Start()
	defer system.Stop()

	system.Send(system.CreateActorRef(callerType, "1"), "go")

	select {
	case rsp := <-done:
		if rsp != "echo:second" {
			t.Fatalf("stale response leaked into next request, got: %v", rsp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("test timeout")
	}
}

// 回归测试：TickInterval<=0 时 Stop 不得 panic；Stop 可重复调用
func TestStopWithoutTickerAndDoubleStop(t *testing.T) {
	system := NewSystem(func(c *SystemConfig) {
		c.TickInterval = 0
	})
	system.Start()
	system.Stop()
	system.Stop() // 幂等
	if system.IsRunning() {
		t.Fatal("IsRunning should be false after Stop")
	}
}

func TestRingBufferClearZeroesSlots(t *testing.T) {
	rb := NewRingBuffer[*int](4)
	a, b := 1, 2
	rb.Push(&a)
	rb.Push(&b)
	rb.Clear()
	if rb.Count() != 0 || !rb.IsEmpty() {
		t.Fatal("Clear should empty the buffer")
	}
	for i := 0; i < rb.Size(); i++ {
		if rb.buffer[i] != nil {
			t.Fatalf("Clear should zero slot %d", i)
		}
	}
}

// 回归测试：系统停止后本地投递必须返回错误。
// 修复前 LocalRouter 忽略 Enqueue 的失败返回值，停机期间的消息被静默丢弃，
// 调用方（BatchSend/Request）却以为发送成功。
func TestLocalRouterAfterStopReturnsError(t *testing.T) {
	system := NewSystem(func(sc *SystemConfig) {
		sc.LogFunc = func(LogLevel, string, ...interface{}) {}
		sc.TickInterval = 10 * time.Millisecond
	})
	echoType := ActorTypeStart + 1
	registerEchoActor(system, echoType, 0)
	system.Start()
	system.Stop()

	ref := system.CreateActorRef(echoType, "a")
	if err := system.LocalRouter(&EnvelopeSend{ToActorRef: ref, Message: "x"}); err == nil {
		t.Fatal("LocalRouter should report delivery failure after Stop")
	}
}

// Start 之后的配置类调用必须被拒绝（只记日志，不得生效）。
func TestConfigGuardsRejectAfterStart(t *testing.T) {
	system := NewSystem(func(sc *SystemConfig) {
		sc.LogFunc = func(LogLevel, string, ...interface{}) {}
		sc.TickInterval = 10 * time.Millisecond
	})
	targetType := ActorTypeStart + 1
	system.RegisterActorType(targetType, func() Actor {
		return func(EnvelopeContext) {}
	})
	system.Start()
	defer system.Stop()

	// 三者都应在 Start 之后被拒绝：若 SetRouter(nil) 生效，BatchSend 会因 router 为 nil 而报错
	system.SetRouter(nil)
	system.SetCreateActorRefExFunc(nil)
	system.RegisterActorType(ActorTypeStart+2, func() Actor { return func(EnvelopeContext) {} })

	ref := system.CreateActorRef(targetType, "a")
	if ref == nil {
		t.Fatal("CreateActorRef should keep working after rejected config calls")
	}
	if err := system.BatchSend([]ActorRef{ref}, []interface{}{"x"}); err != nil {
		t.Fatalf("BatchSend failed, router may have been overwritten: %v", err)
	}
}

// Watch/Unwatch 传 nil queue 时静默忽略：不 panic、不注册）。
func TestWatchNilQueueIgnored(t *testing.T) {
	system := NewSystem(func(sc *SystemConfig) {
		sc.LogFunc = func(LogLevel, string, ...interface{}) {}
		sc.TickInterval = 10 * time.Millisecond
	})
	watchType := ActorTypeStart + 1
	registerEchoActor(system, watchType, 0)
	system.Start()
	defer system.Stop()

	ref := system.CreateActorRef(watchType, "a")
	system.Watch(ref, WatchType(1), nil)   // 覆盖 queue == nil 分支
	system.Unwatch(ref, WatchType(1), nil) // 覆盖 queue == nil 分支
	system.Send(ref, "still-works")
	time.Sleep(50 * time.Millisecond)
	if !system.IsRunning() {
		t.Fatal("system should stay running")
	}
}

// Start 之前调用 LocalRouter 必须返回错误而不是 panic。
func TestLocalRouterBeforeStartReturnsError(t *testing.T) {
	system := NewSystem(func(sc *SystemConfig) {
		sc.LogFunc = func(LogLevel, string, ...interface{}) {}
	})
	defer system.Stop()
	ref := system.CreateActorRef(ActorTypeStart+1, "a")
	if err := system.LocalRouter(&EnvelopeSend{ToActorRef: ref, Message: "x"}); err == nil {
		t.Fatal("LocalRouter before Start should return an error")
	}
}

// 内部信封（不外发）的 GetToActorRef 必须返回 nil。
func TestEnvelopeInternalGetToActorRef(t *testing.T) {
	if (&envelopeStopedReport{}).GetToActorRef() != nil {
		t.Fatal("envelopeStopedReport.GetToActorRef should be nil")
	}
	if (&envelopeTick{}).GetToActorRef() != nil {
		t.Fatal("envelopeTick.GetToActorRef should be nil")
	}
}

// System 级别的 5 个日志方法都必须走 LogFunc（LogFunc 在 Start 时生效）。
func TestSystemLogMethods(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	system := NewSystem(func(sc *SystemConfig) {
		sc.LogFunc = func(level LogLevel, format string, args ...interface{}) {
			mu.Lock()
			lines = append(lines, level.String()+" "+fmt.Sprintf(format, args...))
			mu.Unlock()
		}
		sc.TickInterval = 0
		// DefaultStopInterval 归零：否则「关了 tick 却设了回收时间」会在 Start 时
		// 多记一条配置告警，干扰本用例对「5 个日志方法各一条」的断言。
		sc.DefaultStopInterval = 0
	})
	system.RegisterActorType(ActorTypeStart+1, func() Actor { return func(EnvelopeContext) {} })
	system.Start()
	defer system.Stop()
	system.LogDebug("d")
	system.LogInfo("i")
	system.LogWarn("w")
	system.LogError("e")
	system.LogFatal("f")
	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 5 {
		t.Fatalf("expected 5 log lines, got %v", lines)
	}
}

// TickInterval <= 0：不创建 ticker，Stop 时不得 panic（ticker == nil 分支）。
func TestStopWithoutTicker(t *testing.T) {
	system := NewSystem(func(sc *SystemConfig) {
		sc.LogFunc = func(LogLevel, string, ...interface{}) {}
		sc.TickInterval = 0
	})
	system.RegisterActorType(ActorTypeStart+1, func() Actor { return func(EnvelopeContext) {} })
	system.Start()
	system.Stop()
	if system.IsRunning() {
		t.Fatal("system should not be running after Stop")
	}
}

// 停机后本地投递 Notify / BatchSend 也必须报错（不得静默丢弃）。
func TestLocalRouterAfterStopDropsNotifyAndBatch(t *testing.T) {
	system := NewSystem(func(sc *SystemConfig) {
		sc.LogFunc = func(LogLevel, string, ...interface{}) {}
		sc.TickInterval = 10 * time.Millisecond
	})
	registerEchoActor(system, ActorTypeStart+1, 0)
	system.Start()
	system.Stop()

	ref := system.CreateActorRef(ActorTypeStart+1, "a")
	if err := system.LocalRouter(&EnvelopeNotify{
		ToActorRefs: []ActorRef{ref},
		NotifyType:  NotifyTypeWatch,
		Message:     &MsgOnWatchMsg{},
	}); err == nil {
		t.Fatal("Notify after Stop should return an error")
	}
	if err := system.LocalRouter(&EnvelopeBatchSend{
		ToActorRefs: []ActorRef{ref},
		Messages:    []interface{}{"x"},
	}); err == nil {
		t.Fatal("BatchSend after Stop should return an error")
	}
}

// Start 之前允许替换寻址函数，替换必须立即生效。
func TestSetCreateActorRefExFuncBeforeStart(t *testing.T) {
	system := NewSystem(func(sc *SystemConfig) {
		sc.LogFunc = func(LogLevel, string, ...interface{}) {}
	})
	defer system.Stop()
	system.SetCreateActorRefExFunc(func(systemId SystemId, actorType ActorType, actorId ActorId) ActorRef {
		return &ActorRefImpl{SystemId: systemId, ActorType: actorType, ActorId: actorId, GroupSlot: 7}
	})
	ref := system.CreateActorRef(ActorTypeStart+1, "x")
	if ref.GetGroupSlot() != 7 {
		t.Fatalf("custom CreateActorRefExFunc not applied: %v", ref.GetGroupSlot())
	}
}

// 同一 actorType 重复注册：走 Warn 分支且最后一次注册生效。
// 注意：Start 之前的日志走默认 logFunc（LogFunc 在 Start 时才装载），
// 因此这里断言行为而非日志内容。
func TestRegisterActorTypeDuplicateOverrides(t *testing.T) {
	system := NewSystem(func(sc *SystemConfig) {
		sc.LogFunc = func(LogLevel, string, ...interface{}) {}
		sc.TickInterval = 10 * time.Millisecond
	})
	defer system.Stop()
	tp := ActorTypeStart + 1
	var used int32
	system.RegisterActorType(tp, func() Actor {
		return func(ctx EnvelopeContext) {
			if _, ok := ctx.GetMessage().(string); ok {
				atomic.StoreInt32(&used, 1)
			}
		}
	})
	system.RegisterActorType(tp, func() Actor {
		return func(ctx EnvelopeContext) {
			if _, ok := ctx.GetMessage().(string); ok {
				atomic.StoreInt32(&used, 2)
			}
		}
	})
	system.Start()
	system.Send(system.CreateActorRef(tp, "a"), "x")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&used) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&used); got != 2 {
		t.Fatalf("last registration should win, used = %d", got)
	}
}

// newUnstartedSingleGroupSystem 构造一个只有一个 group、且 **不启动 group goroutine**
// 的 system：没人消费 group mailbox，深度只增不减，便于确定性地验证背压上限。
func newUnstartedSingleGroupSystem(t *testing.T, cfgFuncs ...SystemConfigFunc) (*system, *actorGroup) {
	t.Helper()
	cfgFuncs = append(cfgFuncs, func(sc *SystemConfig) {
		sc.GroupCount = 1
		if sc.LogFunc == nil {
			sc.LogFunc = func(LogLevel, string, ...interface{}) {}
		}
	})
	s := NewSystem(cfgFuncs...).(*system)
	s.actorGroups = []*actorGroup{newActorGroup(s)}
	s.groupCount = 1
	// Start() 才会把配置搬到字段上，而 Start 会启动 group goroutine 把 mailbox 抽干。
	// 这里手动搬一次，等价于"已启动但暂停消费"的状态。
	s.mailboxHighWaterMark = s.config.MailboxHighWaterMark
	s.maxMailboxDepth = s.config.MaxMailboxDepth
	// 同理，自定义 LogFunc 也是 Start() 才装载；上面的 cfgFunc 保证它非 nil。
	s.logFunc = s.config.LogFunc
	return s, s.actorGroups[0]
}

func testActorRef(actorType ActorType, id ActorId) ActorRef {
	return &ActorRefImpl{SystemId: 0, GroupSlot: 1, ActorType: actorType, ActorId: id}
}

// 回归：group mailbox 必须有深度上限。它是组内全部流量的唯一入口，此前只有 actor
// mailbox 受 MaxMailboxDepth 保护，group 侧无界——配置了背压的用户会误以为已受保护。
func TestGroupMailboxDepthIsBounded(t *testing.T) {
	const depth = 4
	logs := make(chan string, 64)
	s, group := newUnstartedSingleGroupSystem(t, func(sc *SystemConfig) {
		sc.MaxMailboxDepth = depth
		sc.LogFunc = func(level LogLevel, format string, args ...interface{}) {
			if level == ErrorLevel {
				select {
				case logs <- fmt.Sprintf(format, args...):
				default:
				}
			}
		}
	})
	limit := depth * GroupMailboxDepthFactor

	// 前 limit 条必须全部成功
	for i := 0; i < limit; i++ {
		if err := s.LocalRouter(&EnvelopeSend{ToActorRef: testActorRef(ActorTypeStart+1, "a"), Message: "m"}); err != nil {
			t.Fatalf("enqueue %d below the limit should succeed, got %v", i, err)
		}
	}
	if got := group.mailbox.Len(); got != limit {
		t.Fatalf("group mailbox depth = %d, want %d", got, limit)
	}

	// 第 limit+1 条必须被丢弃并返回错误
	err := s.LocalRouter(&EnvelopeSend{ToActorRef: testActorRef(ActorTypeStart+1, "a"), Message: "m"})
	if err == nil {
		t.Fatal("enqueue beyond the limit should fail")
	}
	if got := group.mailbox.Len(); got != limit {
		t.Fatalf("dropped message must not be enqueued, depth = %d, want %d", got, limit)
	}
	select {
	case line := <-logs:
		if !strings.Contains(line, "reached limit") {
			t.Fatalf("expected a drop log, got %q", line)
		}
	default:
		t.Fatal("expected an error log for the dropped message")
	}
}

// 批量/通知分支也要走同一个上限，否则扇出大的路径会绕过背压。
func TestGroupMailboxDepthAppliesToBatchAndNotify(t *testing.T) {
	const depth = 2
	s, group := newUnstartedSingleGroupSystem(t, func(sc *SystemConfig) {
		sc.MaxMailboxDepth = depth
	})
	limit := depth * GroupMailboxDepthFactor
	refs := []ActorRef{testActorRef(ActorTypeStart+1, "a"), testActorRef(ActorTypeStart+1, "b")}

	failed := 0
	for i := 0; i < limit+3; i++ {
		if err := s.LocalRouter(&EnvelopeBatchSend{ToActorRefs: refs, Messages: []interface{}{"m"}}); err != nil {
			failed++
		}
	}
	if failed != 3 {
		t.Fatalf("batch send: %d enqueues failed, want 3 (only %d fit)", failed, limit)
	}
	if got := group.mailbox.Len(); got != limit {
		t.Fatalf("batch send depth = %d, want %d", got, limit)
	}

	// 清空后 notify 分支同样受限
	for group.mailbox.Len() > 0 {
		_, _ = group.mailbox.TryDequeue()
	}
	failed = 0
	for i := 0; i < limit+3; i++ {
		if err := s.LocalRouter(&EnvelopeNotify{
			ToActorRefs: refs,
			NotifyType:  NotifyTypeWatch,
			Message:     &MsgOnWatchMsg{},
		}); err != nil {
			failed++
		}
	}
	if failed != 3 {
		t.Fatalf("notify: %d enqueues failed, want 3 (only %d fit)", failed, limit)
	}
}

// 未配置上限时保持旧行为（无界），避免给既有用户带来行为变化。
func TestGroupMailboxDepthUnlimitedByDefault(t *testing.T) {
	s, group := newUnstartedSingleGroupSystem(t)
	for i := 0; i < 2000; i++ {
		if err := s.LocalRouter(&EnvelopeSend{ToActorRef: testActorRef(ActorTypeStart+1, "a"), Message: "m"}); err != nil {
			t.Fatalf("enqueue %d should succeed when unlimited, got %v", i, err)
		}
	}
	if got := group.mailbox.Len(); got != 2000 {
		t.Fatalf("depth = %d, want 2000", got)
	}
}

// 高水位只告警不丢弃。
func TestGroupMailboxHighWaterMarkWarnsOnly(t *testing.T) {
	warns := make(chan string, 64)
	s, _ := newUnstartedSingleGroupSystem(t, func(sc *SystemConfig) {
		sc.MailboxHighWaterMark = 2
		sc.LogFunc = func(level LogLevel, format string, args ...interface{}) {
			if level == WarnLevel {
				select {
				case warns <- fmt.Sprintf(format, args...):
				default:
				}
			}
		}
	})
	mark := 2 * GroupMailboxDepthFactor
	for i := 0; i < mark+1; i++ {
		if err := s.LocalRouter(&EnvelopeSend{ToActorRef: testActorRef(ActorTypeStart+1, "a"), Message: "m"}); err != nil {
			t.Fatalf("high water mark must not drop messages, enqueue %d got %v", i, err)
		}
	}
	select {
	case line := <-warns:
		if !strings.Contains(line, "high water mark") {
			t.Fatalf("expected a high water warning, got %q", line)
		}
	default:
		t.Fatal("expected a high water warning")
	}
}
