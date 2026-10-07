// Copyright (c) 2026 Klaus Post. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package flate

import (
	"errors"
	"fmt"
	"io"
)

// ParamError reports an invalid field of a WriterConfig.
// Field names the offending parameter ("Level" or "Dict")
// and Err describes why the value was rejected.
type ParamError struct {
	Field string
	Err   error
}

func (e *ParamError) Error() string {
	return "flate: invalid " + e.Field + ": " + e.Err.Error()
}

// Unwrap returns the underlying reason the parameter was rejected.
func (e *ParamError) Unwrap() error { return e.Err }

// WriterConfig describes the parameters used to create a deflate Writer.
// It allows the compression level and dictionary selection to be
// validated with Check before any Writer is created, without side
// effects on any output writer.
type WriterConfig struct {
	// Level is the compression level, as accepted by NewWriter:
	// a value in the range [-2, 9].
	Level int

	// Dict is an optional preset dictionary, as accepted by NewWriterDict.
	// A nil dictionary selects a plain Writer (see NewWriter).
	// A non-nil but empty dictionary is invalid.
	Dict []byte
}

// Check validates the configuration without touching any writer.
// It returns nil if the configuration can be used to create a Writer,
// or a *ParamError identifying the specific invalid field.
func (c WriterConfig) Check() error {
	if err := checkLevel(c.Level); err != nil {
		return &ParamError{Field: "Level", Err: err}
	}
	if c.Dict != nil && len(c.Dict) == 0 {
		return &ParamError{Field: "Dict", Err: errors.New("dictionary is empty")}
	}
	return nil
}

// NewWriter validates the configuration and, if it is valid, returns a
// Writer writing compressed data to w, using NewWriterDict when a
// dictionary is configured and NewWriter otherwise.
// If validation fails, no Writer is created and w is left untouched.
func (c WriterConfig) NewWriter(w io.Writer) (*Writer, error) {
	if err := c.Check(); err != nil {
		return nil, err
	}
	if c.Dict != nil {
		return NewWriterDict(w, c.Level, c.Dict)
	}
	return NewWriter(w, c.Level)
}

// checkLevel reports whether level is a valid compression level
// in the range [-2, 9], as documented for NewWriter.
// The error text matches the one returned by the Writer constructors.
func checkLevel(level int) error {
	if level < HuffmanOnly || level > BestCompression {
		return fmt.Errorf("invalid compression level %d: want value in range [-2, 9]", level)
	}
	return nil
}
