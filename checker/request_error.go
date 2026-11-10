package checker

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// safeRequestError redacts the current credential after URL errors have been
// formatted, including nested errors, without changing the request itself.
func safeRequestError(err error, apiKey string) error {
	if err == nil || apiKey == "" {
		return err
	}
	quoted := strconv.Quote(apiKey)
	message := strings.NewReplacer(
		url.QueryEscape(apiKey), "[REDACTED]",
		url.PathEscape(apiKey), "[REDACTED]",
		quoted[1:len(quoted)-1], "[REDACTED]",
		apiKey, "[REDACTED]",
	).Replace(err.Error())
	if message == err.Error() {
		return err
	}
	return &requestDiagnosticError{cause: err, message: message}
}

type requestDiagnosticError struct {
	cause   error
	message string
}

func (e *requestDiagnosticError) Error() string { return e.message }
func (e *requestDiagnosticError) Unwrap() error { return e.cause }
func (e *requestDiagnosticError) Timeout() bool {
	var timeout interface{ Timeout() bool }
	return errors.As(e.cause, &timeout) && timeout.Timeout()
}
