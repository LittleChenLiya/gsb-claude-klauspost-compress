// Copyright 2025+ Klaus Post. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package dictflate

import (
	"bytes"
	"errors"
	"runtime"
	"testing"

	"github.com/klauspost/compress/flate"
)

// TestEmptyDictionary verifies that an empty dictionary works.
func TestEmptyDictionary(t *testing.T) {
	src := []byte("data compressed with an empty dictionary")
	roundTrip(t, nil, src, flate.DefaultCompression)
}

// TestEmptyPayload verifies that an empty compressed payload is rejected.
func TestEmptyPayload(t *testing.T) {
	dict := []byte("dictionary")
	// Craft a valid header but empty payload.
	var hdr [headerSize]byte
	hdr[0] = version
	d := digest(dict)
	for i := 0; i < 8; i++ {
		hdr[1+i] = byte(d >> (8 * i))
	}
	// origLen = 0
	_, err := Decode(dict, hdr[:])
	if err == nil {
		t.Fatal("expected error for empty payload, got nil")
	}
}

// TestOverLongDeclarationNoLargeAlloc verifies that an over-long declared
// length does not allocate a large buffer.
func TestOverLongDeclarationNoLargeAlloc(t *testing.T) {
	dict := []byte("dictionary")
	var hdr [headerSize]byte
	hdr[0] = version
	d := digest(dict)
	for i := 0; i < 8; i++ {
		hdr[1+i] = byte(d >> (8 * i))
	}
	// Declare 1 TiB.
	for i := 9; i < 17; i++ {
		hdr[i] = 0xFF
	}
	opts := &Options{MaxSize: 1 << 20} // 1 MiB

	// Measure heap allocation.
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	_, err := DecodeWithOptions(dict, hdr[:], opts)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}

	runtime.GC()
	runtime.ReadMemStats(&after)
	alloc := after.TotalAlloc - before.TotalAlloc
	// Allow up to 1 MiB of allocation (generous).
	if alloc > 1<<20 {
		t.Fatalf("over-long declaration allocated %d bytes, want <= 1 MiB", alloc)
	}
}

// TestShortHeaderNoLargeAlloc verifies that a short header does not allocate.
func TestShortHeaderNoLargeAlloc(t *testing.T) {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	_, err := Decode([]byte("dict"), make([]byte, headerSize-1))
	if !errors.Is(err, ErrShortHeader) {
		t.Fatalf("expected ErrShortHeader, got %v", err)
	}

	runtime.GC()
	runtime.ReadMemStats(&after)
	alloc := after.TotalAlloc - before.TotalAlloc
	if alloc > 1<<16 {
		t.Fatalf("short header allocated %d bytes, want <= 64 KiB", alloc)
	}
}

// TestZeroMaxSizeUsesDefault verifies that a zero MaxSize uses the default.
func TestZeroMaxSizeUsesDefault(t *testing.T) {
	dict := []byte("dictionary")
	src := []byte("data")
	// Zero MaxSize should use DefaultMaxSize.
	enc, err := EncodeWithOptions(dict, src, flate.DefaultCompression, &Options{MaxSize: 0})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	dec, err := DecodeWithOptions(dict, enc, &Options{MaxSize: 0})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !bytes.Equal(dec, src) {
		t.Fatal("round trip mismatch")
	}
}

// TestNilOptionsUsesDefault verifies that nil options use the default.
func TestNilOptionsUsesDefault(t *testing.T) {
	dict := []byte("dictionary")
	src := []byte("data")
	enc, err := EncodeWithOptions(dict, src, flate.DefaultCompression, nil)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	dec, err := DecodeWithOptions(dict, enc, nil)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !bytes.Equal(dec, src) {
		t.Fatal("round trip mismatch")
	}
}

// TestTrailingDataNoCompression verifies trailing data detection for stored
// (NoCompression) blocks.
func TestTrailingDataNoCompression(t *testing.T) {
	dict := []byte("dictionary for stored block trailing data test")
	src := []byte("data compressed with no compression")
	enc, err := Encode(dict, src, flate.NoCompression)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	trailing := append(append([]byte{}, enc...), 0x00, 0x01, 0x02)
	_, err = Decode(dict, trailing)
	if !errors.Is(err, ErrTrailingData) {
		t.Fatalf("expected ErrTrailingData, got %v", err)
	}
}

// TestRoundTripNoDict verifies that a nil dictionary round-trips.
func TestRoundTripNoDict(t *testing.T) {
	src := []byte("data without a dictionary")
	roundTrip(t, nil, src, flate.DefaultCompression)
}

// TestLargeDeclaredLength verifies that a declared length at the max boundary
// is handled correctly (not rejected as over-long).
func TestLargeDeclaredLength(t *testing.T) {
	dict := []byte("dictionary")
	// Craft a header with origLen = MaxSize (exactly at the boundary).
	var hdr [headerSize]byte
	hdr[0] = version
	d := digest(dict)
	for i := 0; i < 8; i++ {
		hdr[1+i] = byte(d >> (8 * i))
	}
	// Declare MaxSize.
	maxSize := uint64(1 << 20) // 1 MiB
	for i := 9; i < 17; i++ {
		hdr[i] = byte(maxSize >> (8 * (i - 9)))
	}
	opts := &Options{MaxSize: int(maxSize)}
	// The digest check passes, the size check passes (origLen == maxSize),
	// but the payload is empty, so decompression fails.
	_, err := DecodeWithOptions(dict, hdr[:], opts)
	if err == nil {
		t.Fatal("expected error for empty payload, got nil")
	}
}
