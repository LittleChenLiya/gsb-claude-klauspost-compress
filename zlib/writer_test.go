// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zlib

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"testing"
)

var filenames = []string{
	"../testdata/gettysburg.txt",
	"../testdata/e.txt",
	"../testdata/pi.txt",
}

var data = []string{
	"test a reasonable sized string that can be compressed",
}

// Tests that compressing and then decompressing the given file at the given compression level and dictionary
// yields equivalent bytes to the original file.
func testFileLevelDict(t *testing.T, fn string, level int, d string) {
	// Read the file, as golden output.
	golden, err := os.Open(fn)
	if err != nil {
		t.Errorf("%s (level=%d, dict=%q): %v", fn, level, d, err)
		return
	}
	defer golden.Close()
	b0, err0 := io.ReadAll(golden)
	if err0 != nil {
		t.Errorf("%s (level=%d, dict=%q): %v", fn, level, d, err0)
		return
	}
	testLevelDict(t, fn, b0, level, d)
}

func testLevelDict(t *testing.T, fn string, b0 []byte, level int, d string) {
	// Make dictionary, if given.
	var dict []byte
	if d != "" {
		dict = []byte(d)
	}

	// Push data through a pipe that compresses at the write end, and decompresses at the read end.
	piper, pipew := io.Pipe()
	defer piper.Close()
	go func() {
		defer pipew.Close()
		zlibw, err := NewWriterLevelDict(pipew, level, dict)
		if err != nil {
			t.Errorf("%s (level=%d, dict=%q): %v", fn, level, d, err)
			return
		}
		defer zlibw.Close()
		_, err = zlibw.Write(b0)
		if err != nil {
			t.Errorf("%s (level=%d, dict=%q): %v", fn, level, d, err)
			return
		}
	}()
	zlibr, err := NewReaderDict(piper, dict)
	if err != nil {
		t.Errorf("%s (level=%d, dict=%q): %v", fn, level, d, err)
		return
	}
	defer zlibr.Close()

	// Compare the decompressed data.
	b1, err1 := io.ReadAll(zlibr)
	if err1 != nil {
		t.Errorf("%s (level=%d, dict=%q): %v", fn, level, d, err1)
		return
	}
	if len(b0) != len(b1) {
		t.Errorf("%s (level=%d, dict=%q): length mismatch %d versus %d", fn, level, d, len(b0), len(b1))
		return
	}
	for i := range b0 {
		if b0[i] != b1[i] {
			t.Errorf("%s (level=%d, dict=%q): mismatch at %d, 0x%02x versus 0x%02x\n", fn, level, d, i, b0[i], b1[i])
			return
		}
	}
}

func testFileLevelDictReset(t *testing.T, fn string, level int, dict []byte) {
	var b0 []byte
	var err error
	if fn != "" {
		b0, err = os.ReadFile(fn)
		if err != nil {
			t.Errorf("%s (level=%d): %v", fn, level, err)
			return
		}
	}

	// Compress once.
	buf := new(bytes.Buffer)
	var zlibw *Writer
	if dict == nil {
		zlibw, err = NewWriterLevel(buf, level)
	} else {
		zlibw, err = NewWriterLevelDict(buf, level, dict)
	}
	if err == nil {
		_, err = zlibw.Write(b0)
	}
	if err == nil {
		err = zlibw.Close()
	}
	if err != nil {
		t.Errorf("%s (level=%d): %v", fn, level, err)
		return
	}
	out := buf.String()

	// Reset and compress again.
	buf2 := new(bytes.Buffer)
	zlibw.Reset(buf2)
	_, err = zlibw.Write(b0)
	if err == nil {
		err = zlibw.Close()
	}
	if err != nil {
		t.Errorf("%s (level=%d): %v", fn, level, err)
		return
	}
	out2 := buf2.String()

	if out2 != out {
		t.Errorf("%s (level=%d): different output after reset (got %d bytes, expected %d",
			fn, level, len(out2), len(out))
	}
}

func TestWriter(t *testing.T) {
	for i, s := range data {
		b := []byte(s)
		tag := fmt.Sprintf("#%d", i)
		testLevelDict(t, tag, b, DefaultCompression, "")
		testLevelDict(t, tag, b, NoCompression, "")
		testLevelDict(t, tag, b, HuffmanOnly, "")
		for level := BestSpeed; level <= BestCompression; level++ {
			testLevelDict(t, tag, b, level, "")
		}
	}
}

func TestWriterBig(t *testing.T) {
	for i, fn := range filenames {
		testFileLevelDict(t, fn, DefaultCompression, "")
		testFileLevelDict(t, fn, NoCompression, "")
		testFileLevelDict(t, fn, HuffmanOnly, "")
		for level := BestSpeed; level <= BestCompression; level++ {
			testFileLevelDict(t, fn, level, "")
			if level >= 1 && testing.Short() {
				break
			}
		}
		if i == 0 && testing.Short() {
			break
		}
	}
}

func TestWriterDict(t *testing.T) {
	const dictionary = "0123456789."
	for i, fn := range filenames {
		testFileLevelDict(t, fn, DefaultCompression, dictionary)
		testFileLevelDict(t, fn, NoCompression, dictionary)
		testFileLevelDict(t, fn, HuffmanOnly, dictionary)
		for level := BestSpeed; level <= BestCompression; level++ {
			testFileLevelDict(t, fn, level, dictionary)
			if level >= 1 && testing.Short() {
				break
			}
		}
		if i == 0 && testing.Short() {
			break
		}
	}
}

func TestWriterReset(t *testing.T) {
	const dictionary = "0123456789."
	for _, fn := range filenames {
		testFileLevelDictReset(t, fn, NoCompression, nil)
		testFileLevelDictReset(t, fn, DefaultCompression, nil)
		testFileLevelDictReset(t, fn, HuffmanOnly, nil)
		testFileLevelDictReset(t, fn, NoCompression, []byte(dictionary))
		testFileLevelDictReset(t, fn, DefaultCompression, []byte(dictionary))
		testFileLevelDictReset(t, fn, HuffmanOnly, []byte(dictionary))
		if testing.Short() {
			break
		}
		for level := BestSpeed; level <= BestCompression; level++ {
			testFileLevelDictReset(t, fn, level, nil)
		}
	}
}

func TestWriterDictIsUsed(t *testing.T) {
	var input = []byte("Lorem ipsum dolor sit amet, consectetur adipisicing elit, sed do eiusmod tempor incididunt ut labore et dolore magna aliqua.")
	var buf bytes.Buffer
	compressor, err := NewWriterLevelDict(&buf, BestCompression, input)
	if err != nil {
		t.Errorf("error in NewWriterLevelDict: %s", err)
		return
	}
	compressor.Write(input)
	compressor.Close()
	const expectedMaxSize = 25
	output := buf.Bytes()
	if len(output) > expectedMaxSize {
		t.Errorf("result too large (got %d, want <= %d bytes). Is the dictionary being used?", len(output), expectedMaxSize)
	}
}

// TestFlushBoundary verifies that a flush receipt reports the correct
// uncompressed input and compressed output positions, that an empty flush is a
// no-op, and that flush and close are distinct states.
func TestFlushBoundary(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	w.Write([]byte("abc"))
	r1, err := w.FlushBoundary()
	if err != nil {
		t.Fatalf("first FlushBoundary: %v", err)
	}
	if r1.Input != 3 {
		t.Errorf("first flush Input = %d, want 3", r1.Input)
	}
	if r1.Output <= 0 {
		t.Errorf("first flush Output = %d, want > 0", r1.Output)
	}

	w.Write([]byte("def"))
	r2, err := w.FlushBoundary()
	if err != nil {
		t.Fatalf("second FlushBoundary: %v", err)
	}
	if r2.Input != 6 {
		t.Errorf("second flush Input = %d, want 6", r2.Input)
	}
	if r2.Output <= r1.Output {
		t.Errorf("second flush Output = %d, want > first Output %d", r2.Output, r1.Output)
	}

	// Empty flush is a no-op.
	r3, err := w.FlushBoundary()
	if err != nil {
		t.Fatalf("empty FlushBoundary: %v", err)
	}
	if r3.Input != r2.Input || r3.Output != r2.Output {
		t.Errorf("empty flush changed boundary: got (%d,%d), want (%d,%d)", r3.Input, r3.Output, r2.Input, r2.Output)
	}

	// After flush, the writer is still open.
	if _, err := w.Write([]byte("ghi")); err != nil {
		t.Errorf("Write after flush: %v, want nil", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// After close, the writer is closed and rejects new input.
	if _, err := w.Write([]byte("jkl")); err == nil {
		t.Error("Write after close: want error, got nil")
	}

	// The reader should consume the segments.
	r, err := NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got := make([]byte, 9)
	if _, err := io.ReadFull(r, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "abcdefghi" {
		t.Errorf("got %q, want %q", got, "abcdefghi")
	}
}
