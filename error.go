package vactor

type ErrorCode int32

const (
	ErrorCodeSuccess     ErrorCode = 0
	ErrorCodeNormal      ErrorCode = 1
	ErrorCodeTimeout     ErrorCode = 2
	ErrorCodeCustomStart ErrorCode = 100
)

type VAError interface {
	error
	Code() ErrorCode
}

func NewTimeoutError() VAError {
	return &timeoutError{}
}

type timeoutError struct {
}

func (e *timeoutError) Code() ErrorCode {
	return ErrorCodeTimeout
}

func (e *timeoutError) Error() string {
	return "timeout"
}
