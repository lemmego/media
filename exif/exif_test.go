package exif_test

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/lemmego/media/exif"
)

// jpegWith builds a JPEG carrying one EXIF orientation entry, in the given
// byte order.
func jpegWith(t *testing.T, orientation int, order binary.ByteOrder) []byte {
	t.Helper()

	var base bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(0, 0, color.White)
	if err := jpeg.Encode(&base, img, nil); err != nil {
		t.Fatalf("encoding: %v", err)
	}
	raw := base.Bytes()

	tiff := new(bytes.Buffer)
	if order == binary.BigEndian {
		tiff.WriteString("MM")
	} else {
		tiff.WriteString("II")
	}
	binary.Write(tiff, order, uint16(42))
	binary.Write(tiff, order, uint32(8))
	binary.Write(tiff, order, uint16(1))
	binary.Write(tiff, order, uint16(0x0112))
	binary.Write(tiff, order, uint16(3))
	binary.Write(tiff, order, uint32(1))
	binary.Write(tiff, order, uint16(orientation))
	binary.Write(tiff, order, uint16(0))
	binary.Write(tiff, order, uint32(0))

	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	segment := new(bytes.Buffer)
	segment.Write([]byte{0xFF, 0xE1})
	binary.Write(segment, binary.BigEndian, uint16(len(payload)+2))
	segment.Write(payload)

	out := new(bytes.Buffer)
	out.Write(raw[:2])
	out.Write(segment.Bytes())
	out.Write(raw[2:])
	return out.Bytes()
}

func TestEveryOrientationRoundTrips(t *testing.T) {
	for want := 1; want <= 8; want++ {
		for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
			got := exif.FromJPEG(jpegWith(t, want, order))
			if int(got) != want {
				t.Fatalf("orientation %d read back as %d", want, got)
			}
		}
	}
}

// A value outside 1–8 is not an orientation, and applying it would be
// applying an unspecified transform.
func TestAnOutOfRangeOrientationReadsAsNone(t *testing.T) {
	for _, value := range []int{0, 9, 255, 65535} {
		if got := exif.FromJPEG(jpegWith(t, value, binary.LittleEndian)); got != exif.None {
			t.Fatalf("orientation %d read back as %d rather than None", value, got)
		}
	}
}

func TestAJPEGWithNoEXIFReadsAsNone(t *testing.T) {
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil); err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if got := exif.FromJPEG(buffer.Bytes()); got != exif.None {
		t.Fatalf("a JPEG with no EXIF read as %d", got)
	}
}

// None rather than an error, for everything unreadable. Refusing an upload
// because its metadata is malformed would reject files every browser renders.
func TestGarbageReadsAsNoneAndNeverPanics(t *testing.T) {
	valid := jpegWith(t, 6, binary.LittleEndian)

	inputs := [][]byte{
		nil,
		[]byte("not a jpeg at all"),
		{0xFF, 0xD8},
		{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x02},
		append([]byte{0xFF, 0xD8}, bytes.Repeat([]byte{0xFF}, 200)...),
	}
	// Plus every truncation of a valid file, which reaches the cuts a list
	// would not think of: inside the APP1 length, inside the TIFF header,
	// inside the IFD entry.
	for length := 0; length <= len(valid); length++ {
		inputs = append(inputs, valid[:length])
	}

	for index, raw := range inputs {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("input %d (%d bytes) panicked: %v", index, len(raw), recovered)
				}
			}()
			if got := exif.FromJPEG(raw); !got.Valid() {
				t.Fatalf("input %d produced %d, which is not a valid orientation", index, got)
			}
		}()
	}
}

// An XMP APP1 uses the same marker and must not be mistaken for EXIF.
func TestAnXMPSegmentIsNotReadAsEXIF(t *testing.T) {
	var base bytes.Buffer
	if err := jpeg.Encode(&base, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil); err != nil {
		t.Fatalf("encoding: %v", err)
	}
	raw := base.Bytes()

	payload := append([]byte("http://ns.adobe.com/xap/1.0/\x00"), []byte("<x:xmpmeta/>")...)
	segment := new(bytes.Buffer)
	segment.Write([]byte{0xFF, 0xE1})
	binary.Write(segment, binary.BigEndian, uint16(len(payload)+2))
	segment.Write(payload)

	out := new(bytes.Buffer)
	out.Write(raw[:2])
	out.Write(segment.Bytes())
	out.Write(raw[2:])

	if got := exif.FromJPEG(out.Bytes()); got != exif.None {
		t.Fatalf("an XMP segment was read as orientation %d", got)
	}
}

func TestSwapsDimensionsNamesTheDiagonalTransforms(t *testing.T) {
	for value := 1; value <= 8; value++ {
		want := value >= 5
		if got := exif.Orientation(value).SwapsDimensions(); got != want {
			t.Fatalf("orientation %d reports SwapsDimensions()=%v", value, got)
		}
	}
}
