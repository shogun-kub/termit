// Package cred derives the credentials two nodes share.
//
// A pair credential is valid on exactly two servers and nowhere else, so two owners can reach each
// other without either of them learning the other's self credential. It is derived, never stored:
// MN can rebuild it from the master key and the two node ids, and a node receives it at sync, keeps it
// in memory and hands it to Xray.
//
// The derivation is versioned. If it ever has to change, epoch changes with it — every served
// credential moves at once, and an old credential stops matching a new one instead of quietly
// continuing to work.
package cred

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// derivation is the domain separation string. It keeps a pair credential from ever colliding with a
// digest computed for another purpose from the same master key.
const derivation = "termit/pair-credential/v1"

// Pair derives the credential shared by nodes a and b.
//
// The result is a UUID in the canonical textual form, because Xray requires one; the version and
// variant bits are set so that clients and validators treat it as a v4 identifier.
func Pair(master []byte, a, b string, epoch int) (string, error) {
	if len(master) < 32 {
		return "", errors.New("cred: the master key must be at least 32 bytes")
	}
	if a == "" || b == "" {
		return "", errors.New("cred: both node ids are required")
	}
	if a == b {
		return "", errors.New("cred: a node has no pair credential with itself")
	}
	if epoch < 1 {
		return "", errors.New("cred: the epoch starts at 1")
	}
	// Order independence: the pair (a, b) is the same pair as (b, a), or the two owners would derive
	// different credentials for the same relationship.
	low, high := a, b
	if high < low {
		low, high = high, low
	}

	mac := hmac.New(sha256.New, master)
	mac.Write([]byte(derivation))
	mac.Write([]byte{0})
	var epochBytes [8]byte
	binary.BigEndian.PutUint64(epochBytes[:], uint64(epoch))
	mac.Write(epochBytes[:])
	writeField(mac, low)
	writeField(mac, high)
	sum := mac.Sum(nil)

	var raw [16]byte
	copy(raw[:], sum[:16])
	raw[6] = (raw[6] & 0x0f) | 0x40 // version 4
	raw[8] = (raw[8] & 0x3f) | 0x80 // RFC 4122 variant
	return formatUUID(raw), nil
}

// writeField writes a length-prefixed field, so that ("ab","c") and ("a","bc") cannot produce the same
// input to the MAC.
func writeField(mac interface{ Write([]byte) (int, error) }, value string) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = mac.Write(length[:])
	_, _ = mac.Write([]byte(value))
}

func formatUUID(raw [16]byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}
