package vactor

type ErrorCode int32

const (
	ErrorCodeSuccess     ErrorCode = 0
	ErrorCodeTimeout     ErrorCode = 1
	ErrorCodeCustomStart ErrorCode = 100
)

type VAError interface {
	error
	Code() ErrorCode
}

func NewTimeoutError() VAError {
	return &timeoutError{}
}

func NewVAError(errorCode ErrorCode) VAError {
	switch errorCode {
	case ErrorCodeTimeout:
		return &timeoutError{}
	default:
		return nil
	}
}

type timeoutError struct {
}

func (e *timeoutError) Code() ErrorCode {
	return ErrorCodeTimeout
}

func (e *timeoutError) Error() string {
	return "timeout"
}
