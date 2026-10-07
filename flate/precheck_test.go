package flate

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

const precheckDict = "hello world Lorem ipsum dolor sit amet, consectetur " +
	"adipiscing elit, sed do eiusmod tempor incididunt ut labore."

const precheckText = "hello world Lorem ipsum dolor sit amet, how are you?"

// spyWriter records every call without storing the data, so a failed
// construction must leave writes == 0 and bytes == 0.
type spyWriter struct {
	writes int
	bytes  int
}

func (s *spyWriter) Write(p []byte) (int, error) {
	s.writes++
	s.bytes += len(p)
	return len(p), nil
}

// roundtripDict compresses data with the given level and dictionary and
// returns the decompressed result.
func roundtripDict(t *testing.T, level int, dict, data []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	w, err := NewWriterDict(&compressed, level, dict)
	if err != nil {
		t.Fatalf("NewWriterDict: %v", err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	r := NewReaderDict(bytes.NewReader(compressed.Bytes()), dict)
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	return got
}

// TestCheckWriterValidLevelAndDict covers every level accepted by
// NewWriter combined with a non-empty dictionary: the precheck succeeds,
// the resulting config builds a Writer and the compressed output is
// restored exactly by a dictionary-aware reader.
func TestCheckWriterValidLevelAndDict(t *testing.T) {
	for level := ConstantCompression; level <= BestCompression; level++ {
		t.Run(levelName(level), func(t *testing.T) {
			cfg, err := CheckWriter(level, []byte(precheckDict))
			if err != nil {
				t.Fatalf("CheckWriter(%d): unexpected error %v", level, err)
			}
			if cfg.Level() != level {
				t.Errorf("Level() = %d, want %d", cfg.Level(), level)
			}
			if string(cfg.Dict()) != precheckDict {
				t.Errorf("Dict() = %q, want %q", cfg.Dict(), precheckDict)
			}

			var buf bytes.Buffer
			w, err := cfg.NewWriter(&buf)
			if err != nil {
				t.Fatalf("cfg.NewWriter: %v", err)
			}
			if _, err := w.Write([]byte(precheckText)); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if err := w.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if buf.Len() == 0 {
				t.Fatal("expected compressed output, got none")
			}

			r := NewReaderDict(bytes.NewReader(buf.Bytes()), []byte(precheckDict))
			defer r.Close()
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("decompress: %v", err)
			}
			if string(got) != precheckText {
				t.Errorf("decompressed = %q, want %q", got, precheckText)
			}

			// A validated config is reusable for more writers.
			w2, err := cfg.NewWriter(io.Discard)
			if err != nil {
				t.Fatalf("cfg.NewWriter reuse: %v", err)
			}
			w2.Close()
		})
	}

	// A nil dictionary is the NewWriter (no dictionary) case.
	cfg, err := CheckWriter(DefaultCompression, nil)
	if err != nil {
		t.Fatalf("CheckWriter with nil dict: %v", err)
	}
	if cfg.Dict() != nil {
		t.Errorf("Dict() = %v, want nil", cfg.Dict())
	}
}

// TestCheckWriterInvalidLevel checks that out-of-range levels are rejected
// with the specific ErrInvalidLevel item, never produce a usable config
// and never touch the destination writer.
func TestCheckWriterInvalidLevel(t *testing.T) {
	for _, level := range []int{10, -3, -4, 42, -32, 1 << 30} {
		spy := &spyWriter{}
		cfg, err := CheckWriter(level, nil)
		if err == nil {
			t.Fatalf("CheckWriter(%d): expected error, got nil", level)
		}
		if !errors.Is(err, ErrInvalidLevel) {
			t.Errorf("CheckWriter(%d): error %v does not wrap ErrInvalidLevel", level, err)
		}
		if errors.Is(err, ErrEmptyDictionary) {
			t.Errorf("CheckWriter(%d): unexpected ErrEmptyDictionary", level)
		}
		if !bytes.Contains([]byte(err.Error()), []byte("flate: invalid compression level")) {
			t.Errorf("CheckWriter(%d): error %q does not mention the level", level, err.Error())
		}
		// Failed check returns the zero value.
		if cfg.Level() != NoCompression || cfg.Dict() != nil {
			t.Errorf("failed config = %+v, want zero value", cfg)
		}
		// Even if the spy is handed over, no writer is produced.
		if w, err := cfg.NewWriter(spy); !errors.Is(err, ErrInvalidConfig) {
			if err == nil {
				w.Close()
			}
			t.Fatalf("cfg.NewWriter from failed check(%d): got error %v, want ErrInvalidConfig", level, err)
		}
		if spy.writes != 0 || spy.bytes != 0 {
			t.Errorf("level %d: spy saw %d writes/%d bytes, want zero output", level, spy.writes, spy.bytes)
		}
	}
}

// TestCheckWriterEmptyDictionary checks that an explicitly selected but
// empty dictionary is reported as its own invalid item and produces no
// output.
func TestCheckWriterEmptyDictionary(t *testing.T) {
	for _, level := range []int{DefaultCompression, BestSpeed, BestCompression, ConstantCompression} {
		spy := &spyWriter{}
		cfg, err := CheckWriter(level, []byte{})
		if err == nil {
			t.Fatalf("level %d: expected error for empty dict", level)
		}
		if !errors.Is(err, ErrEmptyDictionary) {
			t.Errorf("level %d: error %v does not wrap ErrEmptyDictionary", level, err)
		}
		if errors.Is(err, ErrInvalidLevel) {
			t.Errorf("level %d: unexpected ErrInvalidLevel", level)
		}
		if w, err := cfg.NewWriter(spy); !errors.Is(err, ErrInvalidConfig) {
			if err == nil {
				w.Close()
			}
			t.Fatalf("level %d: cfg.NewWriter error %v, want ErrInvalidConfig", level, err)
		}
		if spy.writes != 0 || spy.bytes != 0 {
			t.Errorf("level %d: spy saw %d writes/%d bytes, want zero output", level, spy.writes, spy.bytes)
		}
	}
}

// TestCheckWriterBothInvalid reports every invalid item in one pass.
func TestCheckWriterBothInvalid(t *testing.T) {
	spy := &spyWriter{}
	cfg, err := CheckWriter(10, []byte{})
	if err == nil {
		t.Fatal("expected error for invalid level and empty dict")
	}
	if !errors.Is(err, ErrInvalidLevel) || !errors.Is(err, ErrEmptyDictionary) {
		t.Errorf("error %v should match both ErrInvalidLevel and ErrEmptyDictionary", err)
	}
	if w, err := cfg.NewWriter(spy); !errors.Is(err, ErrInvalidConfig) {
		if err == nil {
			w.Close()
		}
		t.Fatalf("cfg.NewWriter error %v, want ErrInvalidConfig", err)
	}
	if spy.writes != 0 || spy.bytes != 0 {
		t.Errorf("spy saw %d writes/%d bytes, want zero output", spy.writes, spy.bytes)
	}
}

// TestCheckWriterFailureZeroOutput spies on the existing constructors too:
// an invalid level must leave the destination untouched.
func TestCheckWriterFailureZeroOutput(t *testing.T) {
	spy := &spyWriter{}
	if w, err := NewWriter(spy, 42); err == nil {
		w.Close()
		t.Fatal("NewWriter with invalid level should fail")
	}
	if w, err := NewWriterDict(spy, 42, []byte(precheckDict)); err == nil {
		w.Close()
		t.Fatal("NewWriterDict with invalid level should fail")
	}
	if spy.writes != 0 || spy.bytes != 0 {
		t.Errorf("constructors emitted %d writes/%d bytes on failure", spy.writes, spy.bytes)
	}
}

// TestWriterConfigMatchesConstructor proves a prechecked config builds the
// exact same Writer output as the legacy constructor, and that the legacy
// constructors keep their old semantics.
func TestWriterConfigMatchesConstructor(t *testing.T) {
	dict := []byte(precheckDict)
	data := []byte(precheckText)

	var legacy bytes.Buffer
	w, err := NewWriterDict(&legacy, 6, dict)
	if err != nil {
		t.Fatalf("NewWriterDict: %v", err)
	}
	w.Write(data)
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	cfg, err := CheckWriter(6, dict)
	if err != nil {
		t.Fatalf("CheckWriter: %v", err)
	}
	var viaCfg bytes.Buffer
	w, err = cfg.NewWriter(&viaCfg)
	if err != nil {
		t.Fatalf("cfg.NewWriter: %v", err)
	}
	w.Write(data)
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !bytes.Equal(viaCfg.Bytes(), legacy.Bytes()) {
		t.Errorf("cfg.NewWriter output differs from NewWriterDict:\n%v\n%v", viaCfg.Bytes(), legacy.Bytes())
	}

	// Legacy constructor error text is unchanged.
	_, err = NewWriter(nil, 10)
	if err == nil || err.Error() != "flate: invalid compression level 10: want value in range [-2, 9]" {
		t.Fatalf("NewWriter error = %v, want unchanged message", err)
	}

	// An empty, explicitly supplied dictionary stays accepted by the
	// legacy constructor (its pre-existing behaviour).
	w, err = NewWriterDict(io.Discard, 6, []byte{})
	if err != nil {
		t.Fatalf("NewWriterDict with empty dict changed semantics: %v", err)
	}
	w.Close()

	// And a valid no-dictionary configuration roundtrips unchanged.
	if got := roundtripDict(t, 6, nil, data); string(got) != precheckText {
		t.Errorf("roundtrip = %q, want %q", got, precheckText)
	}
}

// TestWriterConfigTempFileFixture runs the precheck end to end against a
// small file in a temporary directory: the compressed result is written
// to disk and restored from it.
func TestWriterConfigTempFileFixture(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.deflate")

	cfg, err := CheckWriter(6, []byte(precheckDict))
	if err != nil {
		t.Fatalf("CheckWriter: %v", err)
	}

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	w, err := cfg.NewWriter(f)
	if err != nil {
		t.Fatalf("cfg.NewWriter: %v", err)
	}
	if _, err := w.Write([]byte(precheckText)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("writer Close: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("file Close: %v", err)
	}

	compressed, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read temp file: %v", err)
	}
	if len(compressed) == 0 {
		t.Fatal("compressed temp file is empty")
	}
	r := NewReaderDict(bytes.NewReader(compressed), []byte(precheckDict))
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if string(got) != precheckText {
		t.Errorf("decompressed = %q, want %q", got, precheckText)
	}
}

func levelName(level int) string {
	switch level {
	case DefaultCompression:
		return "DefaultCompression"
	case ConstantCompression:
		return "HuffmanOnly"
	case NoCompression:
		return "NoCompression"
	default:
		return strconv.Itoa(level)
	}
}
