package apperr

import (
	"errors"
	"fmt"
	"testing"
)

var errDisk = errors.New("disk is full")

func TestErrorTextForLogs(t *testing.T) {
	err := Wrap(errDisk, CodeUnavailable, "sqlite.Save", "storage is unavailable")

	want := "sqlite.Save: storage is unavailable: disk is full"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
	if New(CodeNotFound, "", "no such device").Error() != "no such device" {
		t.Errorf("an error without op and cause must print the reason only")
	}
}

func TestCodeAndReasonThroughWrapping(t *testing.T) {
	var err error = New(CodeNotFound, "service.Stats", "device not found")
	err = fmt.Errorf("handler: %w", err) // a plain wrap on the way up must not hide the code

	if got := CodeOf(err); got != CodeNotFound {
		t.Errorf("CodeOf = %v, want %v", got, CodeNotFound)
	}
	if got := ReasonOf(err); got != "device not found" {
		t.Errorf("ReasonOf = %q", got)
	}
}

func TestForeignErrorsAreInternal(t *testing.T) {
	err := fmt.Errorf("dial tcp 10.0.0.5:5432: connection refused")

	if CodeOf(err) != CodeUnknown {
		t.Errorf("a foreign error must have CodeUnknown")
	}
	if got := ReasonOf(err); got != "internal error" {
		t.Errorf("internal details leaked to the client: %q", got)
	}
}

func TestUnwrapKeepsTheCause(t *testing.T) {
	err := Wrap(errDisk, CodeUnknown, "op", "internal error")

	if !errors.Is(err, errDisk) {
		t.Error("errors.Is must find the wrapped cause")
	}
}

func TestEmptyReasonFallsBackToInternal(t *testing.T) {
	if got := ReasonOf(New(CodeUnknown, "op", "")); got != "internal error" {
		t.Errorf("ReasonOf = %q", got)
	}
}

func TestCodeString(t *testing.T) {
	if CodeTooManyRequests.String() != "too_many_requests" || Code(200).String() != "unknown" {
		t.Error("unexpected Code.String()")
	}
}
