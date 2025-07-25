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

type vaError struct {
	code ErrorCode
}

func (e *vaError) Code() ErrorCode {
	return e.code
}

func (e *vaError) Error() string {
	return ""
}

func NewVAError(errorCode ErrorCode) VAError {
	return &vaError{
		code: errorCode,
	}
}
