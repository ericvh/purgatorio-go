package core

import "errors"

// Error is an Inferno error string (errstr). The well-known strings are in
// package abi; devices and remote servers may produce any text.
type Error string

func (e Error) Error() string { return string(e) }

// Errstr returns the Inferno error string for err.
func Errstr(err error) string {
	var e Error
	if errors.As(err, &e) {
		return string(e)
	}
	return err.Error()
}
