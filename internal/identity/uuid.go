// Package identity creates internal UUIDv7 identifiers.
package identity

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// NewV7 returns a UUIDv7 using at for its millisecond timestamp.
func NewV7(at time.Time) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate UUID randomness: %w", err)
	}
	millis := uint64(at.UTC().UnixMilli())
	for i := 5; i >= 0; i-- {
		raw[i] = byte(millis)
		millis >>= 8
	}
	raw[6] = (raw[6] & 0x0f) | 0x70
	raw[8] = (raw[8] & 0x3f) | 0x80

	var encoded [36]byte
	hex.Encode(encoded[0:8], raw[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], raw[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], raw[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], raw[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], raw[10:16])
	return string(encoded[:]), nil
}
