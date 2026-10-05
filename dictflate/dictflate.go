// Copyright 2025+ Klaus Post. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package dictflate provides a dictionary-aware DEFLATE compression wrapper.
//
// The format prepends a small fixed-size header containing a version byte,
// a digest of the dictionary, and the original uncompressed length.
// Decompression verifies the dictionary digest before invoking the underlying
// DEFLATE decoder, so a mismatched dictionary is rejected before any flate
// processing occurs and cannot produce seemingly valid output.
//
// The underlying DEFLATE format is not modified; this package uses the
// standard flate.NewWriterDict / flate.NewReaderDict entry points. Only a
// single contiguous data segment is supported.
//
// # Protocol
//
// All integers are little-endian.
//
//	Offset  Size  Field       Description
//	0       1     version     Format version; must be 1.
//	1       8     dictDigest  FNV-1a 64-bit digest of the dictionary.
//	9       8     origLen     Original uncompressed length (uint64).
//	17      ...   payload     DEFLATE compressed stream (RFC 1951).
//
// The header is followed by exactly one DEFLATE stream. No trailing bytes are
// permitted. The dictionary is supplied by the caller in-process and is never
// stored in the frame.
package dictflate

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"io"

	"github.com/klauspost/compress/flate"
)

const (
	// headerSize is the fixed size of the format header.
	headerSize = 1 + 8 + 8 // version + dictDigest + origLen = 17

	// version is the current format version.
	version = 1

	// DefaultMaxSize is the default maximum original size (1 GiB).
	DefaultMaxSize = 1 << 30
)

// Errors returned by this package.
var (
	// ErrShortHeader is returned when the input is shorter than the header.
	ErrShortHeader = errors.New("dictflate: header too short")

	// ErrBadVersion is returned when the header version is unsupported.
	ErrBadVersion = errors.New("dictflate: unsupported version")

	// ErrDictMismatch is returned when the dictionary digest does not match.
	// This is checked before any DEFLATE processing.
	ErrDictMismatch = errors.New("dictflate: dictionary digest mismatch")

	// ErrTooLarge is returned when a declared or actual size exceeds the
	// configured maximum.
	ErrTooLarge = errors.New("dictflate: size exceeds maximum")

	// ErrLengthMismatch is returned when the decompressed length does not
	// match the declared original length.
	ErrLengthMismatch = errors.New("dictflate: decompressed length mismatch")

	// ErrTrailingData is returned when bytes follow the compressed stream.
	ErrTrailingData = errors.New("dictflate: trailing data after compressed stream")

	// ErrCorrupt is returned when the compressed data is corrupt.
	ErrCorrupt = errors.New("dictflate: corrupt compressed data")
)

// Options configures the behavior of Encode and Decode.
type Options struct {
	// MaxSize is the maximum allowed original size. A declared or actual
	// decompressed size exceeding this is rejected. Zero means DefaultMaxSize.
	MaxSize int
}

// maxSize returns the effective maximum size.
func (o *Options) maxSize() int {
	if o == nil || o.MaxSize <= 0 {
		return DefaultMaxSize
	}
	return o.MaxSize
}

// digest returns the FNV-1a 64-bit digest of the dictionary.
func digest(dict []byte) uint64 {
	h := fnv.New64a()
	h.Write(dict)
	return h.Sum64()
}

// Encode compresses src using the provided dictionary and returns the framed
// output. The dictionary is not stored; only its digest is saved.
func Encode(dict, src []byte, level int) ([]byte, error) {
	return EncodeWithOptions(dict, src, level, nil)
}

// EncodeWithOptions is like Encode but allows specifying options.
func EncodeWithOptions(dict, src []byte, level int, opts *Options) ([]byte, error) {
	maxSize := opts.maxSize()
	if len(src) > maxSize {
		return nil, ErrTooLarge
	}

	var buf bytes.Buffer
	buf.Grow(headerSize + len(src)/2)

	// Write header.
	var hdr [headerSize]byte
	hdr[0] = version
	binary.LittleEndian.PutUint64(hdr[1:9], digest(dict))
	binary.LittleEndian.PutUint64(hdr[9:17], uint64(len(src)))
	buf.Write(hdr[:])

	// Compress using the standard DEFLATE entry point.
	w, err := flate.NewWriterDict(&buf, level, dict)
	if err != nil {
		return nil, fmt.Errorf("dictflate: create writer: %w", err)
	}
	if _, err := w.Write(src); err != nil {
		w.Close()
		return nil, fmt.Errorf("dictflate: compress: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("dictflate: close writer: %w", err)
	}

	return buf.Bytes(), nil
}

// Decode decompresses src using the provided dictionary. The dictionary
// digest is verified before any DEFLATE processing, so a mismatched dictionary
// is rejected before entering flate.
func Decode(dict, src []byte) ([]byte, error) {
	return DecodeWithOptions(dict, src, nil)
}

// DecodeWithOptions is like Decode but allows specifying options.
func DecodeWithOptions(dict, src []byte, opts *Options) ([]byte, error) {
	maxSize := opts.maxSize()

	if len(src) < headerSize {
		return nil, ErrShortHeader
	}

	// Parse header.
	if src[0] != version {
		return nil, ErrBadVersion
	}
	storedDigest := binary.LittleEndian.Uint64(src[1:9])
	origLen := binary.LittleEndian.Uint64(src[9:17])

	// Verify dictionary digest BEFORE entering flate.
	if digest(dict) != storedDigest {
		return nil, ErrDictMismatch
	}

	// Reject over-long declarations without allocating a large buffer.
	if origLen > uint64(maxSize) {
		return nil, ErrTooLarge
	}

	// Decompress using the standard DEFLATE entry point.
	payload := src[headerSize:]
	r := bytes.NewReader(payload)
	fr := flate.NewReaderDict(r, dict)
	defer fr.Close()

	// Limit output to the declared length to bound allocation.
	lb := &limitedBuffer{max: int(origLen)}
	var n int64
	var err error
	if wt, ok := fr.(io.WriterTo); ok {
		n, err = wt.WriteTo(lb)
	} else {
		n, err = io.Copy(lb, fr)
	}
	if err != nil {
		return nil, fmt.Errorf("dictflate: decompress: %w", err)
	}

	// Strictly check trailing data.
	if r.Len() > 0 {
		return nil, ErrTrailingData
	}

	// Strictly check length.
	if uint64(n) != origLen {
		return nil, ErrLengthMismatch
	}

	return lb.buf, nil
}

// limitedBuffer is an io.Writer that caps the total bytes written.
type limitedBuffer struct {
	buf []byte
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if len(l.buf)+len(p) > l.max {
		return 0, ErrTooLarge
	}
	l.buf = append(l.buf, p...)
	return len(p), nil
}
