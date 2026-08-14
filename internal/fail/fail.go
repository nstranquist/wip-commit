package fail

import (
	"errors"
	"fmt"
)

type Error struct {
	Code string
	Err  error
}

func (err *Error) Error() string { return err.Err.Error() }
func (err *Error) Unwrap() error { return err.Err }

func New(code, message string) error { return &Error{Code: code, Err: errors.New(message)} }
func Wrap(code string, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Code: code, Err: err}
}

func Code(err error) string {
	var typed *Error
	if errors.As(err, &typed) && typed.Code != "" {
		return typed.Code
	}
	return "INTERNAL_ERROR"
}

func Errorf(code, format string, args ...any) error {
	return New(code, fmt.Sprintf(format, args...))
}
