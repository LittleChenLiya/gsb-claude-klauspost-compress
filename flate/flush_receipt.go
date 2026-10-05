// Copyright (c) 2026 Klaus Post
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package flate

import "errors"

// ErrClosed is returned by FlushBoundary and CloseBoundary when the
// writer has already been closed. A closed writer never accepts new
// input again, even if closing it failed.
var ErrClosed = errors.New("flate: writer is closed")

// FlushReceipt is a boundary receipt describing exactly how far a
// FlushBoundary or CloseBoundary call committed the stream.
//
// A receipt is a flush boundary receipt, not a close receipt: Flush
// and Close are distinct states, reported through the Closed field.
type FlushReceipt struct {
	// Input is the number of uncompressed input bytes consumed via
	// Write and encoded into the stream up to this boundary.
	Input int64

	// Output is the number of compressed bytes actually committed to
	// the underlying writer up to this boundary. If the underlying
	// writer short-writes or fails, only the accepted prefix is
	// counted; the missing tail is never reported as committed.
	Output int64

	// Closed reports whether this boundary closed the stream.
	// It is false for FlushBoundary and true once CloseBoundary
	// (or Close) has been invoked, even if closing failed.
	Closed bool
}

// FlushBoundary flushes any pending data to the underlying writer,
// like Flush, and returns a receipt describing the boundary.
//
// The receipt reports the uncompressed input position encoded so far
// and the compressed output position actually committed to the
// underlying writer. If the underlying writer short-writes or fails,
// the returned error is non-nil, Receipt.Output only counts the
// committed prefix, and the error is sticky: the failed input is never
// automatically re-compressed or re-sent, and later calls keep
// returning the same error.
//
// Empty flushes have a deterministic policy: every FlushBoundary call
// emits exactly one sync marker (like Flush), even when no input is
// pending. Receipt.Input is unchanged and Receipt.Output advances by
// the marker size, so repeated empty flushes advance Output by the
// same amount each time.
func (w *Writer) FlushBoundary() (FlushReceipt, error) {
	if w.d.err == nil && w.d.closed {
		return w.d.receipt(), ErrClosed
	}
	err := w.d.syncFlush()
	return w.d.receipt(), err
}

// CloseBoundary closes the writer, like Close, and returns a receipt
// describing the final boundary. The receipt's Closed field is true,
// distinguishing it from a flush boundary.
//
// If closing fails, the error is sticky: no new input is accepted
// afterwards and the same input is not retried.
func (w *Writer) CloseBoundary() (FlushReceipt, error) {
	if w.d.err == nil && w.d.closed {
		return w.d.receipt(), ErrClosed
	}
	err := w.d.close()
	return w.d.receipt(), err
}

// Boundary returns the receipt for the current boundary without
// performing any I/O. It reflects the last committed flush or close.
func (w *Writer) Boundary() FlushReceipt {
	return w.d.receipt()
}

// receipt assembles the FlushReceipt for the current compressor state.
func (d *compressor) receipt() FlushReceipt {
	return FlushReceipt{
		Input:  d.input,
		Output: d.w.bytesWritten,
		Closed: d.closed,
	}
}
