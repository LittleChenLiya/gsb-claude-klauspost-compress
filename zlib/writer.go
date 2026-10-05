// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zlib

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/adler32"
	"io"

	"github.com/klauspost/compress/flate"
)

// These constants are copied from the flate package, so that code that imports
// "compress/zlib" does not also have to import "compress/flate".
const (
	NoCompression       = flate.NoCompression
	BestSpeed           = flate.BestSpeed
	BestCompression     = flate.BestCompression
	DefaultCompression  = flate.DefaultCompression
	ConstantCompression = flate.ConstantCompression // Deprecated: Use HuffmanOnly.
	HuffmanOnly         = flate.HuffmanOnly
)

// A Writer takes data written to it and writes the compressed
// form of that data to an underlying writer (see NewWriter).
type Writer struct {
	w           io.Writer
	cw          *countingWriter // wraps w to track total bytes written
	level       int
	dict        []byte
	compressor  *flate.Writer
	digest      hash.Hash32
	err         error
	scratch     [4]byte
	wroteHeader bool
	closed      bool
	lastFlushIn int64 // total uncompressed input at the last successful flush
}

// countingWriter wraps an io.Writer and records the total number of bytes
// successfully written to it.
type countingWriter struct {
	w io.Writer
	n int64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	cw.n += int64(n)
	return n, err
}

// NewWriter creates a new Writer.
// Writes to the returned Writer are compressed and written to w.
//
// It is the caller's responsibility to call Close on the Writer when done.
// Writes may be buffered and not flushed until Close.
func NewWriter(w io.Writer) *Writer {
	z, _ := NewWriterLevelDict(w, DefaultCompression, nil)
	return z
}

// NewWriterLevel is like NewWriter but specifies the compression level instead
// of assuming DefaultCompression.
//
// The compression level can be DefaultCompression, NoCompression, HuffmanOnly
// or any integer value between BestSpeed and BestCompression inclusive.
// The error returned will be nil if the level is valid.
func NewWriterLevel(w io.Writer, level int) (*Writer, error) {
	return NewWriterLevelDict(w, level, nil)
}

// NewWriterLevelDict is like NewWriterLevel but specifies a dictionary to
// compress with.
//
// The dictionary may be nil. If not, its contents should not be modified until
// the Writer is closed.
func NewWriterLevelDict(w io.Writer, level int, dict []byte) (*Writer, error) {
	if level < HuffmanOnly || level > BestCompression {
		return nil, fmt.Errorf("zlib: invalid compression level: %d", level)
	}
	cw := &countingWriter{w: w}
	return &Writer{
		w:     cw,
		cw:    cw,
		level: level,
		dict:  dict,
	}, nil
}

// Reset clears the state of the Writer z such that it is equivalent to its
// initial state from NewWriterLevel or NewWriterLevelDict, but instead writing
// to w.
func (z *Writer) Reset(w io.Writer) {
	cw := &countingWriter{w: w}
	z.w = cw
	z.cw = cw
	// z.level and z.dict left unchanged.
	if z.compressor != nil {
		z.compressor.Reset(cw)
	}
	if z.digest != nil {
		z.digest.Reset()
	}
	z.err = nil
	z.scratch = [4]byte{}
	z.wroteHeader = false
	z.closed = false
	z.lastFlushIn = 0
}

// writeHeader writes the ZLIB header.
func (z *Writer) writeHeader() (err error) {
	z.wroteHeader = true
	// ZLIB has a two-byte header (as documented in RFC 1950).
	// The first four bits is the CINFO (compression info), which is 7 for the default deflate window size.
	// The next four bits is the CM (compression method), which is 8 for deflate.
	z.scratch[0] = 0x78
	// The next two bits is the FLEVEL (compression level). The four values are:
	// 0=fastest, 1=fast, 2=default, 3=best.
	// The next bit, FDICT, is set if a dictionary is given.
	// The final five FCHECK bits form a mod-31 checksum.
	switch z.level {
	case -2, 0, 1:
		z.scratch[1] = 0 << 6
	case 2, 3, 4, 5:
		z.scratch[1] = 1 << 6
	case 6, -1:
		z.scratch[1] = 2 << 6
	case 7, 8, 9:
		z.scratch[1] = 3 << 6
	default:
		panic("unreachable")
	}
	if z.dict != nil {
		z.scratch[1] |= 1 << 5
	}
	z.scratch[1] += uint8(31 - binary.BigEndian.Uint16(z.scratch[:2])%31)
	if _, err = z.w.Write(z.scratch[0:2]); err != nil {
		return err
	}
	if z.dict != nil {
		// The next four bytes are the Adler-32 checksum of the dictionary.
		binary.BigEndian.PutUint32(z.scratch[:], adler32.Checksum(z.dict))
		if _, err = z.w.Write(z.scratch[0:4]); err != nil {
			return err
		}
	}
	if z.compressor == nil {
		// Initialize deflater unless the Writer is being reused
		// after a Reset call.
		z.compressor, err = flate.NewWriterDict(z.cw, z.level, z.dict)
		if err != nil {
			return err
		}
		z.digest = adler32.New()
	}
	return nil
}

// errClosed is returned by Write when the Writer has been closed.
var errClosed = errors.New("zlib: writer is closed")

// Write writes a compressed form of p to the underlying io.Writer. The
// compressed bytes are not necessarily flushed until the Writer is closed or
// explicitly flushed.
func (z *Writer) Write(p []byte) (n int, err error) {
	if !z.wroteHeader {
		z.err = z.writeHeader()
	}
	if z.err != nil {
		return 0, z.err
	}
	if z.closed {
		return 0, errClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	n, err = z.compressor.Write(p)
	if err != nil {
		z.err = err
		return
	}
	z.digest.Write(p)
	return
}

// Flush flushes the Writer to its underlying io.Writer.
func (z *Writer) Flush() error {
	if !z.wroteHeader {
		z.err = z.writeHeader()
	}
	if z.err != nil {
		return z.err
	}
	if z.closed {
		return nil
	}
	z.err = z.compressor.Flush()
	if z.err == nil {
		z.lastFlushIn = z.compressor.InputBytes()
	}
	return z.err
}

// FlushBoundary flushes any pending compressed data to the underlying writer
// and returns a receipt describing the boundary reached.
//
// The receipt's Input field is the total number of uncompressed bytes encoded
// up to and including this flush boundary. The Output field is the total number
// of compressed bytes actually written to the underlying writer up to and
// including this flush boundary; if the underlying writer short-writes or
// fails, Output reflects the actual prefix written and the error reports the
// failure.
//
// FlushBoundary does not close the writer or write the zlib checksum; after it
// returns the writer remains open and can accept more input. Use Close to end
// the stream.
//
// If no input has been written since the last flush, FlushBoundary is a no-op
// and returns the current boundary without emitting additional sync bytes.
func (z *Writer) FlushBoundary() (flate.Flushed, error) {
	if z.err != nil {
		return flate.Flushed{Input: z.inputBytes(), Output: z.cw.n}, z.err
	}
	if z.closed {
		return flate.Flushed{Input: z.inputBytes(), Output: z.cw.n}, nil
	}
	if !z.wroteHeader {
		z.err = z.writeHeader()
		if z.err != nil {
			return flate.Flushed{Input: z.inputBytes(), Output: z.cw.n}, z.err
		}
	}
	// The input position is tracked by the compressor.
	in := z.compressor.InputBytes()
	if in == z.lastFlushIn {
		// No new input since the last flush; nothing to encode.
		return flate.Flushed{Input: in, Output: z.cw.n}, nil
	}
	z.err = z.compressor.Flush()
	if z.err == nil {
		z.lastFlushIn = in
	}
	return flate.Flushed{Input: in, Output: z.cw.n}, z.err
}

// inputBytes returns the total uncompressed input encoded, or 0 if the
// compressor has not been initialized.
func (z *Writer) inputBytes() int64 {
	if z.compressor != nil {
		return z.compressor.InputBytes()
	}
	return 0
}

// Close closes the Writer, flushing any unwritten data to the underlying
// io.Writer, but does not close the underlying io.Writer.
func (z *Writer) Close() error {
	if z.err != nil {
		return z.err
	}
	if z.closed {
		return nil
	}
	if !z.wroteHeader {
		z.err = z.writeHeader()
		if z.err != nil {
			return z.err
		}
	}
	z.closed = true
	z.err = z.compressor.Close()
	if z.err != nil {
		return z.err
	}
	checksum := z.digest.Sum32()
	// ZLIB (RFC 1950) is big-endian, unlike GZIP (RFC 1952).
	binary.BigEndian.PutUint32(z.scratch[:], checksum)
	_, z.err = z.w.Write(z.scratch[0:4])
	return z.err
}
