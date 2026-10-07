// Copyright (c) 2026 Klaus Post. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package flate

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// spyWriter records every write so tests can verify that a failed
// precheck leaves the output writer completely untouched.
type spyWriter struct {
	buf     bytes.Buffer
	writes  int
	written int
}

func (s *spyWriter) Write(p []byte) (int, error) {
	s.writes++
	s.written += len(p)
	return s.buf.Write(p)
}

var precheckDict = []byte("the quick brown fox jumps over the lazy dog")

func TestWriterConfigCheckValidWithDict(t *testing.T) {
	cfg := WriterConfig{Level: DefaultCompression, Dict: precheckDict}
	if err := cfg.Check(); err != nil {
		t.Fatalf("Check() returned unexpected error: %v", err)
	}

	spy := &spyWriter{}
	zw, err := cfg.NewWriter(spy)
	if err != nil {
		t.Fatalf("NewWriter() returned unexpected error: %v", err)
	}
	// Compress data that matches the dictionary well.
	payload := []byte(strings.Repeat("the quick brown fox jumps over the lazy dog. ", 64))
	if _, err := zw.Write(payload); err != nil {
		t.Fatalf("Write() returned unexpected error: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close() returned unexpected error: %v", err)
	}
	if spy.written == 0 {
		t.Fatal("expected compressed output on the writer")
	}

	// Decompress with the same dictionary and verify the roundtrip.
	zr := NewReaderDict(bytes.NewReader(spy.buf.Bytes()), precheckDict)
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("ReadAll() returned unexpected error: %v", err)
	}
	if err := zr.Close(); err != nil {
		t.Fatalf("reader Close() returned unexpected error: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("roundtrip mismatch: got %d bytes, want %d bytes", len(got), len(payload))
	}
}

func TestWriterConfigCheckInvalidLevel(t *testing.T) {
	for _, level := range []int{-3, -100, 10, 100} {
		cfg := WriterConfig{Level: level, Dict: precheckDict}
		err := cfg.Check()
		if err == nil {
			t.Fatalf("Check() with level %d: expected error, got nil", level)
		}
		var pe *ParamError
		if !errors.As(err, &pe) {
			t.Fatalf("Check() with level %d: expected *ParamError, got %T", level, err)
		}
		if pe.Field != "Level" {
			t.Fatalf("Check() with level %d: expected Field %q, got %q", level, "Level", pe.Field)
		}

		// A failed precheck must not touch the output writer at all.
		spy := &spyWriter{}
		zw, err := cfg.NewWriter(spy)
		if err == nil {
			t.Fatalf("NewWriter() with level %d: expected error, got nil", level)
		}
		if zw != nil {
			t.Fatalf("NewWriter() with level %d: expected nil Writer, got %v", level, zw)
		}
		if spy.writes != 0 || spy.written != 0 {
			t.Fatalf("NewWriter() with level %d: output writer touched (%d writes, %d bytes)", level, spy.writes, spy.written)
		}
	}
}

func TestWriterConfigCheckEmptyDict(t *testing.T) {
	cfg := WriterConfig{Level: BestSpeed, Dict: []byte{}}
	err := cfg.Check()
	if err == nil {
		t.Fatal("Check() with empty dictionary: expected error, got nil")
	}
	var pe *ParamError
	if !errors.As(err, &pe) {
		t.Fatalf("Check() with empty dictionary: expected *ParamError, got %T", err)
	}
	if pe.Field != "Dict" {
		t.Fatalf("Check() with empty dictionary: expected Field %q, got %q", "Dict", pe.Field)
	}

	// A failed precheck must not touch the output writer at all.
	spy := &spyWriter{}
	zw, err := cfg.NewWriter(spy)
	if err == nil {
		t.Fatal("NewWriter() with empty dictionary: expected error, got nil")
	}
	if zw != nil {
		t.Fatalf("NewWriter() with empty dictionary: expected nil Writer, got %v", zw)
	}
	if spy.writes != 0 || spy.written != 0 {
		t.Fatalf("NewWriter() with empty dictionary: output writer touched (%d writes, %d bytes)", spy.writes, spy.written)
	}
}

func TestWriterConfigNoDictMatchesNewWriter(t *testing.T) {
	payload := []byte(strings.Repeat("hello world, hello deflate! ", 100))

	// A config without a dictionary must behave exactly like NewWriter.
	var want bytes.Buffer
	zw, err := NewWriter(&want, 5)
	if err != nil {
		t.Fatalf("NewWriter() returned unexpected error: %v", err)
	}
	if _, err := zw.Write(payload); err != nil {
		t.Fatalf("Write() returned unexpected error: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close() returned unexpected error: %v", err)
	}

	var got bytes.Buffer
	zw, err = (WriterConfig{Level: 5}).NewWriter(&got)
	if err != nil {
		t.Fatalf("WriterConfig.NewWriter() returned unexpected error: %v", err)
	}
	if _, err := zw.Write(payload); err != nil {
		t.Fatalf("Write() returned unexpected error: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close() returned unexpected error: %v", err)
	}
	if !bytes.Equal(got.Bytes(), want.Bytes()) {
		t.Fatal("WriterConfig.NewWriter() output differs from NewWriter() output")
	}
}

func TestWriterConfigExistingConstructorsUnchanged(t *testing.T) {
	// The existing constructors must keep their return semantics:
	// same error text for invalid levels, nil error for valid ones.
	const wantErr = "flate: invalid compression level 42: want value in range [-2, 9]"
	if _, err := NewWriter(io.Discard, 42); err == nil || err.Error() != wantErr {
		t.Fatalf("NewWriter() error = %v, want %q", err, wantErr)
	}
	if _, err := NewWriterDict(io.Discard, 42, precheckDict); err == nil || err.Error() != wantErr {
		t.Fatalf("NewWriterDict() error = %v, want %q", err, wantErr)
	}
	for _, level := range []int{-2, -1, 0, 1, 9} {
		if _, err := NewWriter(io.Discard, level); err != nil {
			t.Fatalf("NewWriter() with level %d returned unexpected error: %v", level, err)
		}
	}
}
