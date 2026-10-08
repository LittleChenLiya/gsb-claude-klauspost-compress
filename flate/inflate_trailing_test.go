// Copyright (c) 2026 Klaus Post. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package flate

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"testing/iotest"
)

// magic is arbitrary non-DEFLATE data appended after a stream; a reader that
// over-reads would swallow or corrupt it.
var magic = []byte("MAGIC\x00\xff\x1f\x8b")

// onlyReader hides ReadByte from a bytes.Reader so makeReader must insert its
// own buffering. It still serves small, fragmented Reads.
type onlyReader struct{ r io.Reader }

func (r onlyReader) Read(p []byte) (int, error) { return r.r.Read(p) }

// TestTrailingBytes compresses a small payload and appends a magic trailer,
// then checks decompression and trailer recovery through both a ByteReader
// and a plain, fragmented io.Reader, including truncated streams.
func TestTrailingBytes(t *testing.T) {
	payload := []byte("hello, DEFLATE trailing bytes")

	for _, lvl := range []int{-1, 0, 1, 6, 9} {
		var comp bytes.Buffer
		w, err := NewWriter(&comp, lvl)
		if err != nil {
			t.Fatalf("level %d: %v", lvl, err)
		}
		if _, err := w.Write(payload); err != nil {
			t.Fatalf("level %d: %v", lvl, err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("level %d: %v", lvl, err)
		}
		stream := comp.Bytes()
		full := append(append([]byte(nil), stream...), magic...)

		readers := map[string]func([]byte) io.Reader{
			"ByteReader": func(b []byte) io.Reader {
				// bytes.Reader implements io.ByteReader: no wrapper buffering.
				return bytes.NewReader(b)
			},
			"plain fragmented Reader": func(b []byte) io.Reader {
				// No ReadByte, and each Read returns at most a few bytes,
				// forcing the inserted bufio.Reader to refill often.
				return onlyReader{r: iotest.HalfReader(bytes.NewReader(b))}
			},
		}

		for rname, mk := range readers {
			t.Run(levelName(lvl)+"/"+rname, func(t *testing.T) {
				r := NewReaderOpts(mk(full), WithTrailingBytes()).(TrailingReader)

				// Trailer is not available before the stream ends.
				if _, err := r.TrailingBytes(); !errors.Is(err, ErrNotEOF) {
					t.Fatalf("TrailingBytes before EOF: got %v, want %v", err, ErrNotEOF)
				}

				got, err := io.ReadAll(r)
				if err != nil {
					t.Fatalf("decompress: %v", err)
				}
				if !bytes.Equal(got, payload) {
					t.Fatalf("payload mismatch: got %q, want %q", got, payload)
				}

				tail, err := r.TrailingBytes()
				if err != nil {
					t.Fatalf("TrailingBytes after EOF: %v", err)
				}
				if !bytes.Equal(tail, magic) {
					t.Fatalf("trailer mismatch: got % x, want % x", tail, magic)
				}
				if err := r.Close(); err != nil {
					t.Fatalf("close: %v", err)
				}
			})

			// A truncated stream must surface the original decompression
			// error from both Read and TrailingBytes, never a trailer.
			t.Run(levelName(lvl)+"/"+rname+"/truncated", func(t *testing.T) {
				cut := len(stream) / 2
				if cut == 0 {
					cut = 1
				}
				r := NewReaderOpts(mk(stream[:cut]), WithTrailingBytes()).(TrailingReader)
				_, readErr := io.ReadAll(r)
				if readErr == nil || readErr == io.EOF {
					t.Fatalf("read truncated stream: got %v, want a non-EOF error", readErr)
				}
				tail, err := r.TrailingBytes()
				if err != readErr {
					t.Fatalf("TrailingBytes on bad stream: got %v, want read error %v", err, readErr)
				}
				if tail != nil {
					t.Fatalf("TrailingBytes on bad stream returned % x", tail)
				}
			})
		}

		// With a plain io.Reader the inserted buffering must hand back
		// everything past the stream, byte-for-byte. Trailers up to the
		// read-ahead buffer size are recovered entirely from the buffer;
		// ByteReader inputs are not bounded at all.
		for _, n := range []int{0, 1, 100, 1024, trailingBufSize - 1, trailingBufSize} {
			trailer := bytes.Repeat([]byte{0xa5}, n)
			input := append(append([]byte(nil), stream...), trailer...)
			r := NewReaderOpts(onlyReader{r: bytes.NewReader(input)}, WithTrailingBytes()).(TrailingReader)
			if _, err := io.Copy(io.Discard, r); err != nil {
				t.Fatalf("trailer len %d: decompress: %v", n, err)
			}
			got, err := r.TrailingBytes()
			if err != nil {
				t.Fatalf("trailer len %d: %v", n, err)
			}
			if !bytes.Equal(got, trailer) {
				t.Fatalf("trailer len %d: got %d bytes", n, len(got))
			}
		}
		// A ByteReader recovers even an oversized trailer directly from
		// the source reader.
		bigTrailer := bytes.Repeat([]byte{0x5a}, trailingBufSize+7)
		input := append(append([]byte(nil), stream...), bigTrailer...)
		r := NewReaderOpts(bytes.NewReader(input), WithTrailingBytes()).(TrailingReader)
		if _, err := io.Copy(io.Discard, r); err != nil {
			t.Fatalf("big trailer: decompress: %v", err)
		}
		got, err := r.TrailingBytes()
		if err != nil || !bytes.Equal(got, bigTrailer) {
			t.Fatalf("big trailer: got %d bytes, err %v", len(got), err)
		}
	}
}

// TestTrailingBytesPaddingBits checks hand-crafted streams where the final
// block ends mid-byte: padding bits must be discarded and only subsequent
// bytes belong to the trailer.
func TestTrailingBytesPaddingBits(t *testing.T) {
	cases := []struct {
		name   string
		stream []byte
	}{
		{
			// Empty fixed-Huffman final block: BFINAL=1, BTYPE=01, then the
			// 7-bit EOB symbol: 10 bits total, so the top 6 bits of the
			// second byte are padding and must be dropped.
			name:   "empty-fixed-block",
			stream: []byte{0x03, 0x00},
		},
		{
			// BFINAL=1, BTYPE=01, single literal 'A' followed by the 7-bit
			// EOB: 18 bits total, so the final 6 bits are padding inside the
			// third byte and must not appear in the trailer.
			name:   "literal-fixed-block",
			stream: []byte{0x73, 0x04, 0x00},
		},
	}
	wantOut := [][]byte{nil, []byte("A")}

	for i, tc := range cases {
		full := append(append([]byte(nil), tc.stream...), magic...)
		for rname, mk := range map[string]func([]byte) io.Reader{
			"ByteReader": func(b []byte) io.Reader { return bytes.NewReader(b) },
			"Reader":     func(b []byte) io.Reader { return onlyReader{r: bytes.NewReader(b)} },
		} {
			t.Run(tc.name+"/"+rname, func(t *testing.T) {
				r := NewReaderOpts(mk(full), WithTrailingBytes()).(TrailingReader)
				out, err := io.ReadAll(r)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(out, wantOut[i]) {
					t.Fatalf("output: got %q, want %q", out, wantOut[i])
				}
				tail, err := r.TrailingBytes()
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(tail, magic) {
					t.Fatalf("trailer: got % x, want % x", tail, magic)
				}
			})
		}
	}
}

// TestTrailingBytesStoredBlocks covers BTYPE=00 blocks, which have no padding
// bits, as well as streams made of several non-final blocks.
func TestTrailingBytesStoredBlocks(t *testing.T) {
	var comp bytes.Buffer
	w, err := NewWriter(&comp, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("stored block payload")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	full := append(comp.Bytes(), magic...)

	for name, mk := range map[string]func() io.Reader{
		"ByteReader": func() io.Reader { return bytes.NewReader(full) },
		"Reader":     func() io.Reader { return onlyReader{r: iotest.OneByteReader(bytes.NewReader(full))} },
	} {
		t.Run(name, func(t *testing.T) {
			r := NewReaderOpts(mk(), WithTrailingBytes()).(TrailingReader)
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != "stored block payload" {
				t.Fatalf("payload: %q", got)
			}
			tail, err := r.TrailingBytes()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(tail, magic) {
				t.Fatalf("trailer: got % x, want % x", tail, magic)
			}
		})
	}
}

// TestTrailingBytesReset ensures the option survives Reset and a reused
// reader still tracks its own buffering.
func TestTrailingBytesReset(t *testing.T) {
	var a, b bytes.Buffer
	w1, _ := NewWriter(&a, 6)
	w1.Write([]byte("first message"))
	w1.Close()
	w2, _ := NewWriter(&b, 1)
	w2.Write([]byte("second"))
	w2.Close()

	mk := func(buf *bytes.Buffer) io.Reader {
		return onlyReader{r: iotest.HalfReader(bytes.NewReader(append(append([]byte(nil), buf.Bytes()...), magic...)))}
	}

	r := NewReaderOpts(mk(&a), WithTrailingBytes()).(TrailingReader)
	out, err := io.ReadAll(r)
	if err != nil || string(out) != "first message" {
		t.Fatalf("first: %q %v", out, err)
	}
	tail, err := r.TrailingBytes()
	if err != nil || !bytes.Equal(tail, magic) {
		t.Fatalf("first trailer: % x %v", tail, err)
	}

	if err := r.(Resetter).Reset(mk(&b), nil); err != nil {
		t.Fatal(err)
	}
	out, err = io.ReadAll(r)
	if err != nil || string(out) != "second" {
		t.Fatalf("second: %q %v", out, err)
	}
	tail, err = r.TrailingBytes()
	if err != nil || !bytes.Equal(tail, magic) {
		t.Fatalf("second trailer: % x %v", tail, err)
	}
}

// fileReader is an os.File viewed strictly as an io.Reader: it has no
// ReadByte or Seek methods exposed, so trailing recovery must rely solely on
// the decoder's inserted buffering.
type fileReader struct{ f *os.File }

func (r fileReader) Read(p []byte) (int, error) { return r.f.Read(p) }

// TestTrailingBytesTempFile is a temp-file fixture: a small compressed
// stream followed by MAGIC is written to disk, then read back with a tiny
// 1-byte application buffer through a plain io.Reader. It verifies the
// decompressed payload, the recovered MAGIC trailer and, on a truncated
// copy of the file, the original decompression error.
func TestTrailingBytesTempFile(t *testing.T) {
	payload := []byte("temp-file payload")
	var comp bytes.Buffer
	w, err := NewWriter(&comp, 6)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data := append(append([]byte(nil), comp.Bytes()...), magic...)

	dir := t.TempDir()
	path := filepath.Join(dir, "stream+magic.deflate")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	decode := func(p string) ([]byte, []byte, error) {
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		r := NewReaderOpts(fileReader{f}, WithTrailingBytes()).(TrailingReader)
		// Read into a single-byte buffer to prove no large read-ahead is
		// required by the caller.
		var out bytes.Buffer
		buf := make([]byte, 1)
		for {
			n, rerr := r.Read(buf)
			out.Write(buf[:n])
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				return out.Bytes(), nil, rerr
			}
		}
		tail, err := r.TrailingBytes()
		return out.Bytes(), tail, err
	}

	out, tail, err := decode(path)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(out, payload) {
		t.Fatalf("payload: got %q, want %q", out, payload)
	}
	if !bytes.Equal(tail, magic) {
		t.Fatalf("trailer from file: got % x, want % x", tail, magic)
	}
	t.Logf("temp file: %d compressed+%d trailer bytes -> %q, trailer=%q",
		len(comp.Bytes()), len(magic), out, tail)

	// Truncated file: cut the stream in half and keep no trailer.
	bad := filepath.Join(dir, "truncated.deflate")
	if err := os.WriteFile(bad, comp.Bytes()[:len(comp.Bytes())/2], 0o600); err != nil {
		t.Fatal(err)
	}
	out, tail, err = decode(bad)
	if err == nil || err == io.EOF {
		t.Fatalf("truncated file: got %v, want a non-EOF decompression error", err)
	}
	if tail != nil {
		t.Fatalf("truncated file returned trailer % x", tail)
	}
	t.Logf("truncated file after %d output bytes: %v", len(out), err)
}

func levelName(lvl int) string {
	switch lvl {
	case -1:
		return "levelDefault"
	case 0:
		return "levelStored"
	default:
		return "level" + string(rune('0'+lvl))
	}
}
