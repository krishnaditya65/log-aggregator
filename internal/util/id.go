package util

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

func NewID(prefix string) string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return prefix + "-" + time.Now().UTC().Format("20060102150405") + "-" + hex.EncodeToString(buf[:])
}
