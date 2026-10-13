package media

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// countGIFFrames walks a GIF's block structure and counts its image
// descriptors.
//
// This exists because the standard library offers no cheap way to ask. The
// only function that reports a frame count is gif.DecodeAll, which holds
// every frame as an *image.Paletted at once — so a few megabytes of LZW, well
// inside any sane upload limit, can expand to several gigabytes of resident
// memory. A GIF with ten thousand 1000×1000 frames is under the eight
// megabytes this application already allows.
//
// Walking the blocks reads the structure and never decompresses anything, so
// the cost is proportional to the file and the allocation is nil. The format
// is fixed and small enough that this is a short function rather than a
// parser: every block is either an extension or an image, and both are
// followed by a chain of length-prefixed sub-blocks terminated by a zero.
//
// A malformed file is an error, never a panic — these are somebody else's
// bytes.
func countGIFFrames(raw []byte) (int, error) {
	// Header plus the logical screen descriptor.
	if len(raw) < 13 {
		return 0, errors.New("media: the GIF is shorter than its own header")
	}
	at := 10

	// A global colour table, if the packed field says there is one. Its size
	// is 3 bytes per entry and the entry count is 2^(n+1), n being the low
	// three bits.
	if raw[at]&0x80 != 0 {
		at += 3 << ((raw[at] & 0x07) + 1)
	}
	at += 3 // packed, background colour index, pixel aspect ratio

	frames := 0
	for {
		if at >= len(raw) {
			// Truncated. Whatever was counted is still the honest answer:
			// the frames that are present are the frames a decoder will
			// find, and refusing here would reject a file every browser
			// renders.
			return max(frames, 1), nil
		}

		switch raw[at] {
		case 0x3B: // trailer
			return max(frames, 1), nil

		case 0x21: // extension introducer
			at += 2 // introducer and label
			next, err := skipSubBlocks(raw, at)
			if err != nil {
				return max(frames, 1), nil
			}
			at = next

		case 0x2C: // image descriptor — one frame
			frames++
			at++
			if at+9 > len(raw) {
				return frames, nil
			}
			packed := raw[at+8]
			at += 9
			if packed&0x80 != 0 {
				at += 3 << ((packed & 0x07) + 1)
			}
			at++ // LZW minimum code size
			next, err := skipSubBlocks(raw, at)
			if err != nil {
				return frames, nil
			}
			at = next

		default:
			// Not a block boundary, so the structure is not one this walk
			// can follow. Reporting one frame means the image is treated as
			// a still, which is the conservative answer: it is decoded with
			// the standard library, which reads the first frame, and the
			// submitted bytes are preserved either way.
			return max(frames, 1), nil
		}
	}
}

// skipSubBlocks advances past a chain of length-prefixed sub-blocks and
// returns the offset just after its zero terminator.
func skipSubBlocks(raw []byte, at int) (int, error) {
	for {
		if at >= len(raw) {
			return 0, errors.New("media: the GIF ends inside a sub-block chain")
		}
		size := int(raw[at])
		at++
		if size == 0 {
			return at, nil
		}
		at += size
	}
}

// gifDimensions reads the logical screen descriptor.
//
// Used only to cross-check the header against the decoded first frame, since
// a frame may be smaller than the canvas it is drawn on.
func gifDimensions(raw []byte) (width, height int, err error) {
	if len(raw) < 10 {
		return 0, 0, fmt.Errorf("media: the GIF is shorter than its own header")
	}
	return int(binary.LittleEndian.Uint16(raw[6:8])),
		int(binary.LittleEndian.Uint16(raw[8:10])), nil
}
