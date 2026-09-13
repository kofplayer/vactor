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
