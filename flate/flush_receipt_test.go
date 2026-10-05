// Copyright (c) 2026 Klaus Post
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package flate_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/klauspost/compress/flate"
)

// TestFlushBoundarySegments writes two small segments with a flush in
// between and verifies that a Reader can consume each segment as soon as
// its flush boundary has been committed.
func TestFlushBoundarySegments(t *testing.T) {
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	r1, err := w.FlushBoundary()
	if err != nil {
		t.Fatal(err)
	}
	if r1.Input != 3 {
		t.Errorf("first flush: input position = %d, want 3", r1.Input)
	}
	if r1.Closed {
		t.Error("first flush: Closed = true, want false (flush is not close)")
	}
	if r1.Output != int64(buf.Len()) {
		t.Errorf("first flush: output position = %d, want %d (bytes actually written)", r1.Output, buf.Len())
	}
	seg1 := append([]byte(nil), buf.Bytes()...)

	if _, err := w.Write([]byte("def")); err != nil {
		t.Fatal(err)
	}
	r2, err := w.FlushBoundary()
	if err != nil {
		t.Fatal(err)
	}
	if r2.Input != 6 {
		t.Errorf("second flush: input position = %d, want 6", r2.Input)
	}
	if r2.Output <= r1.Output {
		t.Errorf("second flush: output position = %d, want > %d", r2.Output, r1.Output)
	}
	if r2.Output != int64(buf.Len()) {
		t.Errorf("second flush: output position = %d, want %d", r2.Output, buf.Len())
	}

	// The first segment alone must be consumable by a Reader.
	zr := flate.NewReader(bytes.NewReader(seg1))
	got, err := io.ReadAll(zr)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("reading first segment: %v", err)
	}
	if string(got) != "abc" {
		t.Errorf("first segment decoded as %q, want %q", got, "abc")
	}
	zr.Close()

	rc, err := w.CloseBoundary()
	if err != nil {
		t.Fatal(err)
	}
	if !rc.Closed {
		t.Error("close receipt: Closed = false, want true (close differs from flush)")
	}
	if rc.Input != 6 {
		t.Errorf("close receipt: input position = %d, want 6", rc.Input)
	}
	if rc.Output != int64(buf.Len()) {
		t.Errorf("close receipt: output position = %d, want %d", rc.Output, buf.Len())
	}

	// The full stream decodes to both segments.
	zr = flate.NewReader(&buf)
	got, err = io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "abcdef" {
		t.Errorf("full stream decoded as %q, want %q", got, "abcdef")
	}
	zr.Close()
}

// shortWriter accepts all bytes until failAfter, then short-writes
// (accepting only a prefix) and reports the actually accepted count.
type shortWriter struct {
	buf       bytes.Buffer
	failAfter int
	failed    bool
}

func (s *shortWriter) Write(p []byte) (int, error) {
	if !s.failed && s.buf.Len()+len(p) > s.failAfter {
		s.failed = true
		n := s.failAfter - s.buf.Len()
		if n > len(p) {
			n = len(p)
		}
		s.buf.Write(p[:n])
		return n, nil // short write without an error
	}
	return s.buf.Write(p)
}

// TestFlushBoundaryShortWrite verifies that a short write on the
// second flush is not reported as fully committed: the receipt only
// counts the accepted prefix, the error is sticky, and the same input
// is not re-compressed or re-sent.
func TestFlushBoundaryShortWrite(t *testing.T) {
	var first bytes.Buffer
	// Find the committed size of the first segment.
	{
		w, _ := flate.NewWriter(&first, flate.DefaultCompression)
		w.Write([]byte("abc"))
		if _, err := w.FlushBoundary(); err != nil {
			t.Fatal(err)
		}
	}

	sw := &shortWriter{failAfter: first.Len() + 2}
	w, err := flate.NewWriter(sw, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("abc"))
	r1, err := w.FlushBoundary()
	if err != nil {
		t.Fatal(err)
	}
	if r1.Output != int64(first.Len()) {
		t.Fatalf("first flush: output = %d, want %d", r1.Output, first.Len())
	}

	w.Write([]byte("def"))
	r2, err := w.FlushBoundary()
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("second flush: err = %v, want io.ErrShortWrite", err)
	}
	// The receipt must not claim the full commit: only the accepted
	// prefix is counted.
	if want := int64(sw.buf.Len()); r2.Output != want {
		t.Errorf("second flush: output = %d, want %d (accepted prefix only)", r2.Output, want)
	}
	// Input position still reflects what was encoded.
	if r2.Input != 6 {
		t.Errorf("second flush: input = %d, want 6", r2.Input)
	}

	// The error is sticky: no automatic re-run of the same input.
	committed := sw.buf.Len()
	r3, err := w.FlushBoundary()
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("third flush: err = %v, want sticky io.ErrShortWrite", err)
	}
	if r3.Output != r2.Output || sw.buf.Len() != committed {
		t.Errorf("failed input was retried: output moved from %d to %d", r2.Output, r3.Output)
	}
	if _, err := w.Write([]byte("ghi")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("write after failed flush: err = %v, want io.ErrShortWrite", err)
	}
}

// failWriter always fails with its err.
type failWriter struct{ err error }

func (f failWriter) Write([]byte) (int, error) { return 0, f.err }

var errBoom = errors.New("boom")

// TestCloseBoundaryFailure verifies that close and flush states
// differ, and that no new input is accepted after a failed close.
func TestCloseBoundaryFailure(t *testing.T) {
	w, err := flate.NewWriter(failWriter{errBoom}, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("abc"))
	if _, err := w.FlushBoundary(); !errors.Is(err, errBoom) {
		t.Fatalf("flush: err = %v, want %v", err, errBoom)
	}

	// Close reports the same sticky failure, but as a closed boundary.
	rc, err := w.CloseBoundary()
	if !errors.Is(err, errBoom) {
		t.Fatalf("close: err = %v, want %v", err, errBoom)
	}
	if !rc.Closed {
		t.Error("close receipt: Closed = false, want true")
	}

	// No new input is accepted after the failed close.
	if _, err := w.Write([]byte("def")); !errors.Is(err, errBoom) {
		t.Fatalf("write after failed close: err = %v, want %v", err, errBoom)
	}
	if _, err := w.FlushBoundary(); !errors.Is(err, errBoom) {
		t.Fatalf("flush after failed close: err = %v, want %v", err, errBoom)
	}
	if got := w.Boundary(); got.Input != 3 || !got.Closed {
		t.Errorf("boundary after failed close = %+v, want Input 3 and Closed", got)
	}
}

// TestFlushBoundaryEmpty verifies the deterministic policy for
// repeated empty flushes: input position is unchanged and output
// advances by the same sync marker size every time.
func TestFlushBoundaryEmpty(t *testing.T) {
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("abc"))
	r1, err := w.FlushBoundary()
	if err != nil {
		t.Fatal(err)
	}
	r2, err := w.FlushBoundary()
	if err != nil {
		t.Fatal(err)
	}
	r3, err := w.FlushBoundary()
	if err != nil {
		t.Fatal(err)
	}
	if r2.Input != r1.Input || r3.Input != r1.Input {
		t.Errorf("empty flush changed input position: %d, %d, %d", r1.Input, r2.Input, r3.Input)
	}
	d1, d2 := r2.Output-r1.Output, r3.Output-r2.Output
	if d1 <= 0 {
		t.Errorf("empty flush did not emit a sync marker: output delta %d", d1)
	}
	if d1 != d2 {
		t.Errorf("empty flush not deterministic: deltas %d and %d", d1, d2)
	}
	if r3.Output != int64(buf.Len()) {
		t.Errorf("receipt output = %d does not match committed bytes %d", r3.Output, buf.Len())
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// After Close, boundary calls report ErrClosed, not a new flush.
	if _, err := w.FlushBoundary(); !errors.Is(err, flate.ErrClosed) {
		t.Fatalf("flush after close: err = %v, want flate.ErrClosed", err)
	}
	if _, err := w.CloseBoundary(); !errors.Is(err, flate.ErrClosed) {
		t.Fatalf("second close: err = %v, want flate.ErrClosed", err)
	}
	if got := w.Boundary(); !got.Closed || got.Input != 3 {
		t.Errorf("final boundary = %+v, want Input 3 and Closed", got)
	}
}

// TestFlushBoundaryReset verifies that Reset restarts both positions.
func TestFlushBoundaryReset(t *testing.T) {
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("abc"))
	if _, err := w.FlushBoundary(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	buf.Reset()
	w.Reset(&buf)
	w.Write([]byte("de"))
	r, err := w.FlushBoundary()
	if err != nil {
		t.Fatal(err)
	}
	if r.Input != 2 || r.Closed {
		t.Errorf("receipt after reset = %+v, want Input 2, not Closed", r)
	}
	if r.Output != int64(buf.Len()) {
		t.Errorf("output after reset = %d, want %d", r.Output, buf.Len())
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}
