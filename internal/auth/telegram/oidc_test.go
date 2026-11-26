package telegram

import (
	"strings"
	"testing"
)

func TestUserFromClaims(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		claim    string
		username string
		want     User
		wantErr  bool
	}{
		{name: "a normal user", id: "123456789", claim: "Nursultan", username: "nurs", want: User{123456789, "Nursultan", "nurs"}},
		{name: "no username", id: "42", claim: "Nursultan", want: User{ID: 42, Name: "Nursultan"}},
		{name: "spaces are trimmed", id: "42", claim: "  Nursultan  ", username: " nurs ", want: User{42, "Nursultan", "nurs"}},
		{name: "a large id", id: "9007199254740993", claim: "x", want: User{ID: 9007199254740993, Name: "x"}},
		{name: "no id", id: "", wantErr: true},
		{name: "zero", id: "0", wantErr: true},
		{name: "negative", id: "-1", wantErr: true},
		{name: "not a number", id: "abc", wantErr: true},
		{name: "a fraction", id: "1.5", wantErr: true},
		{name: "exponent notation", id: "1e9", wantErr: true},
		{name: "overflow", id: "9223372036854775808", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := userFromClaims(tt.id, tt.claim, tt.username)

			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, want error: %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("user = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCleanTextKeepsRunesAndDropsBrokenUTF8(t *testing.T) {
	long := strings.Repeat("ж", 300) // two bytes per rune: cutting by bytes would break a character

	if got := cleanText(long); got != strings.Repeat("ж", maxNameLen) {
		t.Errorf("a long name has %d runes, want %d", len([]rune(got)), maxNameLen)
	}
	if got := cleanText("a\xffb"); got != "ab" {
		t.Errorf("invalid UTF-8 must be dropped: %q", got)
	}
	if got := cleanText(strings.Repeat("a", maxNameLen)); len(got) != maxNameLen {
		t.Errorf("a name of exactly %d runes must stay whole, got %d", maxNameLen, len(got))
	}
}
