package identity

import (
	"regexp"
	"testing"
	"time"
)

func TestNewV7(t *testing.T) {
	id, err := NewV7(time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Fatalf("NewV7() = %q", id)
	}
}
