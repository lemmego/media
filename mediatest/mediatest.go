// Package mediatest supplies an in-memory [media.Store], a contract suite any
// Store implementation must pass, and image fixtures.
//
// The fixtures exist because almost every interesting case in this package is
// about the *bytes*: a JPEG with an orientation tag, a PNG whose alpha
// channel is present but unused, an animated GIF. Building those by hand at
// each call site is how a test ends up asserting something other than what it
// says.
package mediatest

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/lemmego/media"
)

// Memory is an in-memory Store.
//
// Safe for concurrent use, because the real one is and a test that only
// passes single-threaded is a test that will be run with -race one day.
type Memory struct {
	mu    sync.RWMutex
	files map[string][]byte

	// FailOn makes Write return an error for a path containing this string,
	// which is how the rollback path is exercised: a conversion that fails
	// after earlier ones were written has to leave nothing behind.
	FailOn string
}

// NewMemory builds an empty store.
func NewMemory() *Memory {
	return &Memory{files: make(map[string][]byte)}
}

func (m *Memory) Write(path string, contents []byte) error {
	if m.FailOn != "" && strings.Contains(path, m.FailOn) {
		return fmt.Errorf("mediatest: refusing to write %q", path)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Copied, because the caller owns the slice it passed and a store that
	// aliases it reports whatever the caller does next.
	m.files[path] = bytes.Clone(contents)
	return nil
}

func (m *Memory) Read(path string) (io.ReadCloser, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	contents, ok := m.files[path]
	if !ok {
		return nil, fmt.Errorf("mediatest: %q does not exist", path)
	}
	return io.NopCloser(bytes.NewReader(bytes.Clone(contents))), nil
}

func (m *Memory) Delete(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.files[path]; !ok {
		return fmt.Errorf("mediatest: %q does not exist", path)
	}
	delete(m.files, path)
	return nil
}

func (m *Memory) Exists(path string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.files[path]
	return ok, nil
}

// Paths returns every stored path.
func (m *Memory) Paths() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	paths := make([]string, 0, len(m.files))
	for path := range m.files {
		paths = append(paths, path)
	}
	return paths
}

// Len returns how many files are stored.
func (m *Memory) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.files)
}

// Bytes returns the stored contents.
func (m *Memory) Bytes(t testing.TB, path string) []byte {
	t.Helper()
	m.mu.RLock()
	defer m.mu.RUnlock()
	contents, ok := m.files[path]
	if !ok {
		t.Fatalf("nothing is stored at %q; the store holds %v", path, m.Paths())
	}
	return bytes.Clone(contents)
}

var _ media.Store = (*Memory)(nil)

// Contract runs the Store contract against a fresh store from factory.
//
// Every case is a property [media.Manager] depends on. The one worth naming
// is the last: Delete of a missing path must report it, because
// [media.Manager.Remove] distinguishes "it was not there" from "it could not
// be removed" by asking Exists afterwards, and a Delete that silently
// succeeds makes that distinction meaningless.
func Contract(t *testing.T, factory func(t *testing.T) media.Store) {
	t.Helper()

	cases := []struct {
		name string
		run  func(t *testing.T, store media.Store)
	}{
		{"WriteThenRead", func(t *testing.T, store media.Store) {
			if err := store.Write("a/b.jpg", []byte("hello")); err != nil {
				t.Fatalf("writing: %v", err)
			}
			reader, err := store.Read("a/b.jpg")
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			defer reader.Close()
			got, _ := io.ReadAll(reader)
			if string(got) != "hello" {
				t.Fatalf("read back %q", got)
			}
		}},
		{"WriteReplaces", func(t *testing.T, store media.Store) {
			_ = store.Write("a/b.jpg", []byte("first"))
			if err := store.Write("a/b.jpg", []byte("second")); err != nil {
				t.Fatalf("overwriting: %v", err)
			}
			reader, err := store.Read("a/b.jpg")
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			defer reader.Close()
			got, _ := io.ReadAll(reader)
			if string(got) != "second" {
				t.Fatalf("an overwrite left %q — a re-upload to the same base "+
					"must replace the file, not be ignored", got)
			}
		}},
		{"ExistsAnswersBoth", func(t *testing.T, store media.Store) {
			if exists, err := store.Exists("a/b.jpg"); err != nil || exists {
				t.Fatalf("Exists on a missing path returned (%v, %v)", exists, err)
			}
			_ = store.Write("a/b.jpg", []byte("x"))
			if exists, err := store.Exists("a/b.jpg"); err != nil || !exists {
				t.Fatalf("Exists on a written path returned (%v, %v)", exists, err)
			}
		}},
		{"DeleteRemoves", func(t *testing.T, store media.Store) {
			_ = store.Write("a/b.jpg", []byte("x"))
			if err := store.Delete("a/b.jpg"); err != nil {
				t.Fatalf("deleting: %v", err)
			}
			if exists, _ := store.Exists("a/b.jpg"); exists {
				t.Fatal("the path still exists after Delete")
			}
		}},
		{"ReadOfAMissingPathFails", func(t *testing.T, store media.Store) {
			if _, err := store.Read("nope.jpg"); err == nil {
				t.Fatal("reading a missing path succeeded")
			}
		}},
		{"DeleteOfAMissingPathReportsIt", func(t *testing.T, store media.Store) {
			if err := store.Delete("nope.jpg"); err == nil {
				t.Fatal("deleting a missing path succeeded silently, so " +
					"Manager.Remove cannot tell a failed delete from a file " +
					"that was never written")
			}
		}},
	}

	for _, each := range cases {
		t.Run(each.name, func(t *testing.T) {
			each.run(t, factory(t))
		})
	}
}

// --- fixtures ----------------------------------------------------------

// OpaqueJPEG returns a JPEG of the given size with no transparency and no
// metadata.
func OpaqueJPEG(t testing.TB, width, height int) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := jpeg.Encode(&out, gradient(width, height, 0xFF), &jpeg.Options{Quality: 92}); err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	return out.Bytes()
}

// TransparentPNG returns a PNG with a genuinely translucent pixel.
func TransparentPNG(t testing.TB, width, height int) []byte {
	t.Helper()
	img := gradient(width, height, 0xFF)
	img.Set(0, 0, color.NRGBA{R: 0, G: 0, B: 0, A: 0})
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	return out.Bytes()
}

// OpaquePNG returns a PNG whose colour model has an alpha channel in which
// every pixel is fully opaque.
//
// The case that separates "has an alpha channel" from "is transparent", which
// is most PNGs on the web: a screenshot saved as RGBA with nothing
// translucent in it. Choosing a format by colour model keeps this as a PNG
// several times larger than the JPEG it could be.
func OpaquePNG(t testing.TB, width, height int) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := png.Encode(&out, gradient(width, height, 0xFF)); err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	return out.Bytes()
}

// AnimatedGIF returns a GIF with the given number of frames.
func AnimatedGIF(t testing.TB, width, height, frames int) []byte {
	t.Helper()
	animation := &gif.GIF{}
	for index := 0; index < frames; index++ {
		frame := image.NewPaletted(image.Rect(0, 0, width, height),
			color.Palette{color.Black, color.White})
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				frame.SetColorIndex(x, y, uint8((x+y+index)%2))
			}
		}
		animation.Image = append(animation.Image, frame)
		animation.Delay = append(animation.Delay, 10)
	}
	var out bytes.Buffer
	if err := gif.EncodeAll(&out, animation); err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	return out.Bytes()
}

// OrientedJPEG returns a JPEG carrying the given EXIF orientation.
//
// Built by splicing a hand-assembled APP1 segment in after the start-of-image
// marker, because nothing in the standard library writes EXIF. The segment is
// the minimum a reader must accept: the "Exif\0\0" identifier, a
// little-endian TIFF header, and one IFD0 entry.
func OrientedJPEG(t testing.TB, width, height, orientation int) []byte {
	t.Helper()

	base := OpaqueJPEG(t, width, height)
	if len(base) < 2 || base[0] != 0xFF || base[1] != 0xD8 {
		t.Fatal("the JPEG fixture does not start with a start-of-image marker")
	}

	tiff := new(bytes.Buffer)
	tiff.WriteString("II")                                  // little-endian
	binary.Write(tiff, binary.LittleEndian, uint16(42))     // the magic number
	binary.Write(tiff, binary.LittleEndian, uint32(8))      // offset of IFD0
	binary.Write(tiff, binary.LittleEndian, uint16(1))      // one entry
	binary.Write(tiff, binary.LittleEndian, uint16(0x0112)) // Orientation
	binary.Write(tiff, binary.LittleEndian, uint16(3))      // type SHORT
	binary.Write(tiff, binary.LittleEndian, uint32(1))      // one value
	binary.Write(tiff, binary.LittleEndian, uint16(orientation))
	binary.Write(tiff, binary.LittleEndian, uint16(0)) // the value field's padding
	binary.Write(tiff, binary.LittleEndian, uint32(0)) // no next IFD

	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)

	segment := new(bytes.Buffer)
	segment.Write([]byte{0xFF, 0xE1})
	// The length field counts itself, so it is the payload plus two.
	binary.Write(segment, binary.BigEndian, uint16(len(payload)+2))
	segment.Write(payload)

	out := new(bytes.Buffer)
	out.Write(base[:2])
	out.Write(segment.Bytes())
	out.Write(base[2:])
	return out.Bytes()
}

// HugeHeaderPNG returns a tiny PNG whose header declares an enormous image.
//
// The decode bomb, in the shape that matters: the file passes any byte limit
// and asks a decoder for gigabytes. A real one is produced by an encoder, not
// by mangling bytes — image/png verifies the IHDR checksum in DecodeConfig,
// so a rewritten width without a recomputed CRC is rejected as a corrupt file
// and never reaches the pixel guard at all. Which is worth knowing: the guard
// cannot be tested with a damaged header, only with a well-formed lie.
func HugeHeaderPNG(t testing.TB, width, height uint32) []byte {
	t.Helper()

	raw := OpaquePNG(t, 4, 4)

	// An 8-byte signature, then the IHDR chunk: a 4-byte length, the type,
	// 13 bytes of data beginning with width and height, and a CRC over the
	// type and the data together.
	const (
		typeAt = 8 + 4
		dataAt = typeAt + 4
		crcAt  = dataAt + 13
	)
	if len(raw) < crcAt+4 {
		t.Fatal("the PNG fixture has no IHDR to rewrite")
	}

	binary.BigEndian.PutUint32(raw[dataAt:dataAt+4], width)
	binary.BigEndian.PutUint32(raw[dataAt+4:dataAt+8], height)
	binary.BigEndian.PutUint32(raw[crcAt:crcAt+4],
		crc32.ChecksumIEEE(raw[typeAt:crcAt]))
	return raw
}

// gradient builds a recognisable image, so a rotation or a crop is visible in
// an assertion rather than being a uniform block that looks correct whatever
// happened to it.
func gradient(width, height int, alpha uint8) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.NRGBA{
				R: uint8(x * 255 / max(width-1, 1)),
				G: uint8(y * 255 / max(height-1, 1)),
				B: 0x40,
				A: alpha,
			})
		}
	}
	return img
}
