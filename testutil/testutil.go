// Package testutil 提供 vactor 的测试辅助工具（定位类似标准库的 net/http/httptest），
// 供 vactor 自身与 dvactor 的测试复用。
//
// 典型用法：
//
//	col := &testutil.Collector{}
//	ts := testutil.NewSystem(t, func(s vactor.System) {
//		s.RegisterActorType(100, col.Creator())
//	})
//	ts.Send(ts.CreateActorRef(100, "a"), "hello")
//	msgs := col.WaitForMessages(t, 1, 2*time.Second, "hello delivered")
package testutil

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kofplayer/vactor"
)

// ---------- 日志捕获 ----------

// LogBuffer 捕获 vactor 日志，可用于日志断言与失败排查。
type LogBuffer struct {
	mu    sync.Mutex
	lines []string
}

// NewLogBuffer 创建日志缓冲，其 LogFunc 可直接赋给 SystemConfig.LogFunc。
func NewLogBuffer() *LogBuffer { return &LogBuffer{} }

// LogFunc 实现 vactor.LogFunc。
func (b *LogBuffer) LogFunc(level vactor.LogLevel, format string, args ...interface{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, level.String()+" "+fmt.Sprintf(format, args...))
}

// Logs 返回全部日志行快照。
func (b *LogBuffer) Logs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.lines...)
}

// Contains 判断是否存在包含 substr 的日志行。
func (b *LogBuffer) Contains(substr string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, line := range b.lines {
		if strings.Contains(line, substr) {
			return true
		}
	}
	return false
}

// ---------- TestSystem ----------

// TestSystem 包装 vactor.System 并附加日志捕获能力。
type TestSystem struct {
	vactor.System
	logs *LogBuffer
}

// WrapSystem 包装一个已创建（通常已启动）的 System。
func WrapSystem(s vactor.System, logs *LogBuffer) *TestSystem {
	return &TestSystem{System: s, logs: logs}
}

// Logs 返回捕获的全部日志行。
func (ts *TestSystem) Logs() []string { return ts.logs.Logs() }

// LogContains 判断是否出现过包含 substr 的日志行。
func (ts *TestSystem) LogContains(substr string) bool { return ts.logs.Contains(substr) }

// DumpLogs 把日志逐行写入测试输出（失败排查用）。
func (ts *TestSystem) DumpLogs(t *testing.T) {
	t.Helper()
	for _, line := range ts.logs.Logs() {
		t.Log(line)
	}
}

// ---------- System 构造 ----------

type SystemOpt func(*systemOptions)

type systemOptions struct {
	tickInterval       time.Duration
	stopInterval       time.Duration
	groupCount         uint16
	highWaterMark      int
	maxMailboxDepth    int
	outerQueueMaxDepth int
}

// WithTickInterval 设置 TickInterval（默认 10ms，加快异步超时类行为的触发）。
func WithTickInterval(d time.Duration) SystemOpt {
	return func(o *systemOptions) { o.tickInterval = d }
}

// WithStopInterval 设置 DefaultStopInterval；0 表示永不闲置回收（默认）。
func WithStopInterval(d time.Duration) SystemOpt {
	return func(o *systemOptions) { o.stopInterval = d }
}

// WithGroupCount 设置分组数（默认 4）。
func WithGroupCount(n uint16) SystemOpt {
	return func(o *systemOptions) { o.groupCount = n }
}

// WithMailboxHighWaterMark 设置 mailbox 高水位告警阈值（默认 0，不告警）。
func WithMailboxHighWaterMark(n int) SystemOpt {
	return func(o *systemOptions) { o.highWaterMark = n }
}

// WithMaxMailboxDepth 设置 mailbox 深度上限（默认 0，不限制）。
func WithMaxMailboxDepth(n int) SystemOpt {
	return func(o *systemOptions) { o.maxMailboxDepth = n }
}

// WithOuterQueueMaxDepth 设置外部 watch/事件队列深度上限（默认 0，不限制）。
func WithOuterQueueMaxDepth(n int) SystemOpt {
	return func(o *systemOptions) { o.outerQueueMaxDepth = n }
}

// NewSystem 创建并启动一个测试用 System。setup 在 Start 之前完成类型注册，
// 可为 nil。自动注册 t.Cleanup 停止系统；测试失败时输出最近日志辅助排障。
func NewSystem(t *testing.T, setup func(s vactor.System), opts ...SystemOpt) *TestSystem {
	t.Helper()
	o := systemOptions{tickInterval: 10 * time.Millisecond, stopInterval: 0, groupCount: 4}
	for _, f := range opts {
		if f != nil {
			f(&o)
		}
	}
	logs := NewLogBuffer()
	s := vactor.NewSystem(func(sc *vactor.SystemConfig) {
		sc.TickInterval = o.tickInterval
		sc.DefaultStopInterval = o.stopInterval
		sc.GroupCount = o.groupCount
		sc.MailboxHighWaterMark = o.highWaterMark
		sc.MaxMailboxDepth = o.maxMailboxDepth
		sc.OuterQueueMaxDepth = o.outerQueueMaxDepth
		sc.LogFunc = logs.LogFunc
	})
	if setup != nil {
		setup(s)
	}
	ts := WrapSystem(s, logs)
	s.Start()
	t.Cleanup(func() {
		s.Stop()
		if t.Failed() {
			t.Log("---- test system logs (last 100) ----")
			lines := logs.Logs()
			start := 0
			if len(lines) > 100 {
				start = len(lines) - 100
			}
			for _, line := range lines[start:] {
				t.Log(line)
			}
		}
	})
	return ts
}

// ---------- 消息收集器 ----------

// Collector 收集投递给 actor 的消息（线程安全，同一 actor 的多次激活共享）。
// MsgOnStart/MsgOnStop/MsgOnTick 单独计数，不计入 Messages。
type Collector struct {
	mu     sync.Mutex
	msgs   []interface{}
	starts atomic.Int32
	stops  atomic.Int32
	ticks  atomic.Int32
}

// Observe 实现 vactor.Actor，可直接作为 actor 函数注册。
func (c *Collector) Observe(ctx vactor.EnvelopeContext) {
	switch ctx.GetMessage().(type) {
	case *vactor.MsgOnStart:
		c.starts.Add(1)
		return
	case *vactor.MsgOnStop:
		c.stops.Add(1)
		return
	case *vactor.MsgOnTick:
		c.ticks.Add(1)
		return
	}
	c.mu.Lock()
	c.msgs = append(c.msgs, ctx.GetMessage())
	c.mu.Unlock()
}

// Creator 返回注册用的 actor 工厂：RegisterActorType(tp, col.Creator())。
func (c *Collector) Creator() func() vactor.Actor {
	return func() vactor.Actor { return c.Observe }
}

// Messages 返回至今收到的业务消息副本。
func (c *Collector) Messages() []interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]interface{}(nil), c.msgs...)
}

// Len 返回已收到的业务消息数量。
func (c *Collector) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.msgs)
}

// Starts/Stops/Ticks 返回生命周期消息计数。
func (c *Collector) Starts() int { return int(c.starts.Load()) }
func (c *Collector) Stops() int  { return int(c.stops.Load()) }
func (c *Collector) Ticks() int  { return int(c.ticks.Load()) }

// Reset 清空消息与全部计数。
func (c *Collector) Reset() {
	c.mu.Lock()
	c.msgs = nil
	c.mu.Unlock()
	c.starts.Store(0)
	c.stops.Store(0)
	c.ticks.Store(0)
}

// WaitForMessages 阻塞直到收集到至少 n 条业务消息并返回副本；超时 Fatal。
func (c *Collector) WaitForMessages(t *testing.T, n int, timeout time.Duration, msg string) []interface{} {
	t.Helper()
	WaitFor(t, timeout, fmt.Sprintf("collect %d messages: %s", n, msg), func() bool {
		return c.Len() >= n
	})
	return c.Messages()
}

// ---------- 异步断言 ----------

// WaitFor 每 5ms 轮询 cond 直到返回 true；超时 Fatal。
func WaitFor(t *testing.T, timeout time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v: %s", timeout, msg)
}

// WaitChan 等待 ch 在 timeout 内产出值；超时 Fatal。
func WaitChan[T any](t *testing.T, ch <-chan T, timeout time.Duration, msg string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(timeout):
		t.Fatalf("timeout (%v) waiting: %s", timeout, msg)
		var zero T
		return zero
	}
}

// NoReceive 断言在 d 时间内 ch 上没有任何消息到达。
func NoReceive[T any](t *testing.T, ch <-chan T, d time.Duration, msg string) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("unexpected value %v received: %s", v, msg)
	case <-time.After(d):
	}
}

// ---------- 端口分配 ----------

// FreePorts 分配 n 个当前空闲的 TCP 端口（全部分配完成后统一释放监听）。
func FreePorts(t *testing.T, n int) []int {
	t.Helper()
	listeners := make([]net.Listener, 0, n)
	defer func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}()
	ports := make([]int, 0, n)
	for i := 0; i < n; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("allocate port: %v", err)
		}
		listeners = append(listeners, l)
		ports = append(ports, l.Addr().(*net.TCPAddr).Port)
	}
	return ports
}
