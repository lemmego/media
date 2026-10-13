package media

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"testing"
)

func animation(t *testing.T, frames int) []byte {
	t.Helper()
	out := &gif.GIF{}
	for index := 0; index < frames; index++ {
		frame := image.NewPaletted(image.Rect(0, 0, 8, 8),
			color.Palette{color.Black, color.White})
		frame.SetColorIndex(index%8, 0, 1)
		out.Image = append(out.Image, frame)
		out.Delay = append(out.Delay, 5)
	}
	var buffer bytes.Buffer
	if err := gif.EncodeAll(&buffer, out); err != nil {
		t.Fatalf("encoding: %v", err)
	}
	return buffer.Bytes()
}

// The count has to be right, because it decides whether the submitted bytes
// are preserved — and preserving them is what keeps an animation animated.
func TestFramesAreCountedWithoutDecoding(t *testing.T) {
	for _, frames := range []int{1, 2, 3, 7, 32} {
		raw := animation(t, frames)
		got, err := countGIFFrames(raw)
		if err != nil {
			t.Fatalf("%d frames: %v", frames, err)
		}
		if got != frames {
			t.Fatalf("counted %d frames in a %d-frame GIF", got, frames)
		}

		// Cross-checked against the standard library, which is the only
		// other answer available — and the one this function exists to avoid
		// calling, because it holds every frame in memory at once.
		decoded, err := gif.DecodeAll(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("the fixture does not decode: %v", err)
		}
		if len(decoded.Image) != got {
			t.Fatalf("the walk counted %d and DecodeAll found %d",
				got, len(decoded.Image))
		}
	}
}

// These are somebody else's bytes, so every malformed input must be an answer
// rather than a panic. Sweeping every truncation of a valid file covers the
// cases a hand-written list would miss — a cut inside a sub-block chain,
// inside a colour table, inside an image descriptor.
func TestATruncatedGIFIsAnAnswerAndNotAPanic(t *testing.T) {
	raw := animation(t, 5)
	for length := 0; length <= len(raw); length++ {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("truncating to %d bytes panicked: %v", length, recovered)
				}
			}()
			frames, err := countGIFFrames(raw[:length])
			if err != nil {
				return // a refusal is fine
			}
			if frames < 1 {
				t.Fatalf("truncating to %d bytes counted %d frames", length, frames)
			}
		}()
	}
}

func TestGarbageIsNotAGIF(t *testing.T) {
	for _, raw := range [][]byte{
		nil,
		[]byte("GIF89a"),
		bytes.Repeat([]byte{0xFF}, 64),
		append([]byte("GIF89a"), bytes.Repeat([]byte{0x2C}, 50)...),
	} {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("%d bytes of garbage panicked: %v", len(raw), recovered)
				}
			}()
			if frames, err := countGIFFrames(raw); err == nil && frames < 1 {
				t.Fatalf("garbage counted %d frames", frames)
			}
		}()
	}
}

func TestGIFDimensionsReadTheLogicalScreen(t *testing.T) {
	var buffer bytes.Buffer
	frame := image.NewPaletted(image.Rect(0, 0, 37, 19),
		color.Palette{color.Black, color.White})
	if err := gif.Encode(&buffer, frame, nil); err != nil {
		t.Fatalf("encoding: %v", err)
	}
	width, height, err := gifDimensions(buffer.Bytes())
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if width != 37 || height != 19 {
		t.Fatalf("got %d×%d, want 37×19", width, height)
	}
}
