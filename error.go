package vactor

import "fmt"

type ErrorCode int32

const (
	ErrorCodeSuccess ErrorCode = 0
	// ErrorCodeTimeout 同步/异步请求等待超时。
	ErrorCodeTimeout ErrorCode = 1
	// ErrorCodeInvalidActor 目标 actor 已失效（SetSelfInvalid）或不存在。
	ErrorCodeInvalidActor ErrorCode = 2
	// ErrorCodeSystemNotStarted System 尚未 Start（或已 Stop）时发送消息。
	ErrorCodeSystemNotStarted ErrorCode = 3
	// ErrorCodeHandlerPanic actor 处理请求消息时 panic，且用户未调用 Response，
	// 框架代为回错以保证请求方不悬挂。
	ErrorCodeHandlerPanic ErrorCode = 4
	// ErrorCodeSelfRequest actor 向自身发起同步 Request。该请求永远等不到结果：
	// 请求信封进的是 mailbox，而本 actor 正阻塞在等待响应上，处理不到它。
	// 框架直接返回本错误而不是让调用方干等到超时（timeout<=0 时更是永久挂死）。
	ErrorCodeSelfRequest ErrorCode = 5
	// ErrorCodeMailboxOverflow 投递因 mailbox 达到 MaxMailboxDepth 而被丢弃。
	// 与 ErrorCodeSystemNotStarted 分开：两者的处置方式完全不同——前者要扩容/提速
	// 消费者或调大上限，后者是生命周期用法问题。混用一个码会让调用方误判。
	ErrorCodeMailboxOverflow ErrorCode = 6
	// ErrorCodeCustomStart 用户自定义错误码起点。
	ErrorCodeCustomStart ErrorCode = 100
)

type VAError interface {
	error
	Code() ErrorCode
}

type vaError struct {
	code ErrorCode
}

func (e *vaError) Code() ErrorCode {
	return e.code
}

func (e *vaError) Error() string {
	return fmt.Sprintf("VaError(code=%d)", e.code)
}

func NewVAError(errorCode ErrorCode) VAError {
	return &vaError{
		code: errorCode,
	}
}
