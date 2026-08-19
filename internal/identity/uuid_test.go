package identity

import (
	"encoding/hex"
	"regexp"
	"sync"
	"testing"
	"time"
)

func TestNewV7(t *testing.T) {
	at := time.Date(2026, 8, 19, 12, 0, 0, 123000000, time.FixedZone("fixture", -3*60*60))
	id, err := NewV7(at)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Fatalf("NewV7() = %q", id)
	}
	raw := decodeUUID(t, id)
	var encodedMillis uint64
	for _, value := range raw[:6] {
		encodedMillis = encodedMillis<<8 | uint64(value)
	}
	if encodedMillis != uint64(at.UTC().UnixMilli()) {
		t.Fatalf("encoded timestamp = %d, want %d", encodedMillis, at.UTC().UnixMilli())
	}
	if raw[6]>>4 != 7 {
		t.Fatalf("version = %d, want 7", raw[6]>>4)
	}
	if raw[8]>>6 != 2 {
		t.Fatalf("variant bits = %02b, want 10", raw[8]>>6)
	}
}

func TestNewV7ConcurrentUniqueness(t *testing.T) {
	const count = 1000
	at := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	ids := make(chan string, count)
	errors := make(chan error, count)
	var group sync.WaitGroup
	for range count {
		group.Add(1)
		go func() {
			defer group.Done()
			id, err := NewV7(at)
			if err != nil {
				errors <- err
				return
			}
			ids <- id
		}()
	}
	group.Wait()
	close(ids)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	seen := make(map[string]struct{}, count)
	for id := range ids {
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate UUIDv7 %q", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != count {
		t.Fatalf("generated %d unique IDs, want %d", len(seen), count)
	}
}

func TestNewV7EncodesCallerProvidedClockRegression(t *testing.T) {
	later, err := NewV7(time.UnixMilli(2000))
	if err != nil {
		t.Fatal(err)
	}
	earlier, err := NewV7(time.UnixMilli(1000))
	if err != nil {
		t.Fatal(err)
	}
	if earlier[:13] >= later[:13] {
		t.Fatalf("regressive caller clock was not preserved: earlier=%q later=%q", earlier, later)
	}
}

func decodeUUID(t *testing.T, id string) [16]byte {
	t.Helper()
	compact := regexp.MustCompile(`-`).ReplaceAllString(id, "")
	decoded, err := hex.DecodeString(compact)
	if err != nil {
		t.Fatal(err)
	}
	var raw [16]byte
	copy(raw[:], decoded)
	return raw
}
