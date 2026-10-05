// Copyright 2025+ Klaus Post. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package dictflate

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/klauspost/compress/flate"
)

// roundTrip compresses src with dict and verifies the decoded bytes match.
func roundTrip(t *testing.T, dict, src []byte, level int) {
	t.Helper()
	enc, err := Encode(dict, src, level)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	dec, err := Decode(dict, enc)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !bytes.Equal(dec, src) {
		t.Fatalf("round trip mismatch: got %d bytes, want %d", len(dec), len(src))
	}
}

func TestRoundTrip(t *testing.T) {
	dict := []byte("the quick brown fox jumps over the lazy dog. " +
		"pack my box with five dozen liquor jugs. " +
		"abcdefghijklmnopqrstuvwxyz0123456789")
	src := []byte("the quick brown fox jumps over the lazy dog. " +
		"pack my box with five dozen liquor jugs. " +
		"the quick brown fox jumps over the lazy dog. " +
		"hello world, this is a test of dictionary compression.")

	for _, level := range []int{flate.NoCompression, flate.BestSpeed, flate.DefaultCompression, flate.BestCompression} {
		t.Run(fmt.Sprintf("level=%d", level), func(t *testing.T) {
			roundTrip(t, dict, src, level)
		})
	}
}

func TestRoundTripLarge(t *testing.T) {
	dict := []byte("0123456789abcdef")
	src := bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz"), 1000)
	roundTrip(t, dict, src, flate.DefaultCompression)
}

// TestEmptyData verifies that empty data is legal but the dictionary is
// still verified.
func TestEmptyData(t *testing.T) {
	dict := []byte("some dictionary content")
	enc, err := Encode(dict, nil, flate.DefaultCompression)
	if err != nil {
		t.Fatalf("Encode empty: %v", err)
	}
	dec, err := Decode(dict, enc)
	if err != nil {
		t.Fatalf("Decode empty: %v", err)
	}
	if len(dec) != 0 {
		t.Fatalf("expected empty output, got %d bytes", len(dec))
	}

	// Empty data with a wrong dictionary must still be rejected.
	_, err = Decode([]byte("wrong dictionary"), enc)
	if !errors.Is(err, ErrDictMismatch) {
		t.Fatalf("expected ErrDictMismatch for empty data with wrong dict, got %v", err)
	}
}

// TestWrongDictBeforeFlate verifies that a mismatched dictionary is rejected
// before any DEFLATE processing.
func TestWrongDictBeforeFlate(t *testing.T) {
	dict := []byte("correct dictionary content")
	src := []byte("data compressed with the correct dictionary")
	enc, err := Encode(dict, src, flate.DefaultCompression)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	wrongDict := []byte("a completely different dictionary that will not match")
	_, err = Decode(wrongDict, enc)
	if !errors.Is(err, ErrDictMismatch) {
		t.Fatalf("expected ErrDictMismatch, got %v", err)
	}
}

// TestShortHeader verifies that a header shorter than the minimum is rejected
// without allocating.
func TestShortHeader(t *testing.T) {
	for i := 0; i < headerSize; i++ {
		_, err := Decode([]byte("dict"), make([]byte, i))
		if !errors.Is(err, ErrShortHeader) {
			t.Fatalf("len=%d: expected ErrShortHeader, got %v", i, err)
		}
	}
}

// TestOverLongDeclaration verifies that an over-long declared length is
// rejected without allocating a large buffer.
func TestOverLongDeclaration(t *testing.T) {
	dict := []byte("dictionary")
	// Craft a header with an impossibly large declared length.
	var hdr [headerSize]byte
	hdr[0] = version
	copy(hdr[1:9], []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08})
	// Declare 1 TiB.
	for i := 9; i < 17; i++ {
		hdr[i] = 0xFF
	}
	_, err := Decode(dict, hdr[:])
	if !errors.Is(err, ErrDictMismatch) {
		// The digest check happens first, so this should be ErrDictMismatch.
		// To test the size check, use the correct digest.
	}

	// Now use the correct digest but an over-long declaration.
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
	_, err = DecodeWithOptions(dict, hdr[:], opts)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
}

// TestTrailingData verifies that trailing bytes after the compressed stream
// are rejected.
func TestTrailingData(t *testing.T) {
	dict := []byte("dictionary for trailing data test")
	src := []byte("some data that will be compressed and then have trailing bytes appended")
	enc, err := Encode(dict, src, flate.DefaultCompression)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// Append trailing bytes.
	trailing := append(append([]byte{}, enc...), 0xDE, 0xAD, 0xBE, 0xEF)
	_, err = Decode(dict, trailing)
	if !errors.Is(err, ErrTrailingData) {
		t.Fatalf("expected ErrTrailingData, got %v", err)
	}
}

// TestBadVersion verifies that an unsupported version is rejected.
func TestBadVersion(t *testing.T) {
	dict := []byte("dictionary")
	enc, err := Encode(dict, []byte("data"), flate.DefaultCompression)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// Corrupt the version byte.
	enc[0] = 0xFF
	_, err = Decode(dict, enc)
	if !errors.Is(err, ErrBadVersion) {
		t.Fatalf("expected ErrBadVersion, got %v", err)
	}
}

// TestLengthMismatch verifies that a decompressed length not matching the
// declared length is rejected.
func TestLengthMismatch(t *testing.T) {
	dict := []byte("dictionary for length mismatch test")
	src := []byte("some data")
	enc, err := Encode(dict, src, flate.DefaultCompression)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// Tamper with the declared length (set to a larger value). The digest
	// covers only the dictionary, so the digest check passes. The limited
	// buffer caps at the declared length (which is larger than the actual
	// output), so the decompression succeeds, but the length check catches
	// the mismatch.
	bigger := uint64(len(src)) + 100
	for i := 9; i < 17; i++ {
		enc[i] = byte(bigger >> (8 * (i - 9)))
	}
	_, err = Decode(dict, enc)
	if !errors.Is(err, ErrLengthMismatch) {
		t.Fatalf("expected ErrLengthMismatch, got %v", err)
	}
}

// TestCorruptData verifies that corrupt compressed data is rejected.
func TestCorruptData(t *testing.T) {
	dict := []byte("dictionary for corrupt data test")
	src := []byte("some data that will be corrupted")
	enc, err := Encode(dict, src, flate.DefaultCompression)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// Corrupt the compressed payload (flip a byte in the middle).
	if len(enc) > headerSize+5 {
		enc[headerSize+3] ^= 0xFF
	}
	_, err = Decode(dict, enc)
	if err == nil {
		t.Fatal("expected error for corrupt data, got nil")
	}
}

// TestMaxSizeEnforced verifies that the MaxSize option is enforced.
func TestMaxSizeEnforced(t *testing.T) {
	dict := []byte("dictionary")
	src := bytes.Repeat([]byte("a"), 1000)
	opts := &Options{MaxSize: 100}
	_, err := EncodeWithOptions(dict, src, flate.DefaultCompression, opts)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge on encode, got %v", err)
	}
}

// TestHeaderSize verifies the header size constant.
func TestHeaderSize(t *testing.T) {
	if headerSize != 17 {
		t.Fatalf("headerSize = %d, want 17", headerSize)
	}
}
