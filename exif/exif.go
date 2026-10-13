// Package exif reads the one EXIF field an image pipeline cannot ignore:
// orientation.
//
// It is deliberately not a general EXIF library. Re-encoding an image
// discards every metadata block, which is most of why this project
// re-encodes — a photograph taken on a phone carries GPS coordinates, a
// device serial number and a timestamp, and serving those bytes back
// unchanged publishes all three to everybody who views the page.
//
// But discarding metadata is not safe on its own. A phone does not rotate the
// pixels it writes; it stores the sensor's landscape frame and adds a tag
// asking the viewer for a quarter turn. image/jpeg ignores that tag, and so
// does every browser once the tag is gone. So stripping EXIF without first
// applying orientation turns every portrait photograph on its side — a
// privacy fix that visibly breaks the feed.
//
// Hence: read this one field, apply it to the pixels, and then let
// re-encoding throw the rest away.
package exif

import "encoding/binary"

// Orientation is the EXIF orientation value, 1 through 8.
//
// The values are the TIFF ones and their names describe what the *viewer*
// must do, not what the camera did:
//
//	1  no transform            5  transpose (mirror along the main diagonal)
//	2  mirror horizontally     6  rotate 90° clockwise
//	3  rotate 180°             7  transverse
//	4  mirror vertically       8  rotate 270° clockwise
type Orientation int

// None is the value meaning "the pixels are already the right way up". It is
// also what every reader here returns when there is no EXIF at all, so a
// caller has one case to handle rather than two.
const None Orientation = 1

// Valid reports whether o is a value this package knows how to apply.
func (o Orientation) Valid() bool { return o >= 1 && o <= 8 }

// SwapsDimensions reports whether applying o exchanges width and height.
//
// True for the four diagonal transforms. A caller that computes a target size
// before rotating needs this, and getting it wrong is how a portrait
// photograph ends up with a landscape thumbnail box.
func (o Orientation) SwapsDimensions() bool {
	return o == 5 || o == 6 || o == 7 || o == 8
}

// FromJPEG returns the orientation declared in a JPEG's EXIF block, or None.
//
// Never an error: an unreadable or absent tag means "leave the pixels alone",
// which is both the safe answer and the common one. Refusing an upload
// because its metadata is malformed would reject files every browser renders.
func FromJPEG(raw []byte) Orientation {
	segment := app1(raw)
	if segment == nil {
		return None
	}
	return fromTIFF(segment)
}

// app1 finds the payload of the first APP1 segment carrying an EXIF header.
//
// A JPEG is a sequence of markers: 0xFF then a marker byte. Most carry a
// two-byte big-endian length that includes itself. The scan stops at
// start-of-scan, because everything after it is entropy-coded pixel data and
// a 0xFF in it is not a marker.
func app1(raw []byte) []byte {
	if len(raw) < 4 || raw[0] != 0xFF || raw[1] != 0xD8 {
		return nil // not a JPEG
	}
	at := 2
	for at+4 <= len(raw) {
		if raw[at] != 0xFF {
			// Out of step with the marker structure. A JPEG may pad with
			// 0xFF bytes before a marker, so skip one and look again rather
			// than giving up.
			at++
			continue
		}
		marker := raw[at+1]
		switch {
		case marker == 0xFF:
			at++
			continue
		case marker == 0xD8 || (marker >= 0xD0 && marker <= 0xD9):
			// Standalone markers with no length field.
			at += 2
			continue
		case marker == 0xDA:
			// Start of scan: no markers past here.
			return nil
		}

		length := int(binary.BigEndian.Uint16(raw[at+2 : at+4]))
		if length < 2 || at+2+length > len(raw) {
			return nil
		}
		payload := raw[at+4 : at+2+length]

		// "Exif\0\0" — the six-byte identifier that distinguishes an EXIF
		// APP1 from an XMP one, which uses the same marker.
		if marker == 0xE1 && len(payload) >= 6 &&
			string(payload[:4]) == "Exif" && payload[4] == 0x00 {
			return payload[6:]
		}
		at += 2 + length
	}
	return nil
}

// fromTIFF reads tag 0x0112 out of IFD0 of a TIFF header.
//
// EXIF embeds a complete little TIFF file: a byte-order mark, a magic number,
// an offset to the first image file directory, and then that directory as a
// count followed by twelve-byte entries.
func fromTIFF(tiff []byte) Orientation {
	if len(tiff) < 8 {
		return None
	}

	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return None
	}
	if order.Uint16(tiff[2:4]) != 42 {
		// The magic number, chosen by the TIFF authors as an answer rather
		// than a value. A file without it is not one.
		return None
	}

	offset := int(order.Uint32(tiff[4:8]))
	if offset < 8 || offset+2 > len(tiff) {
		return None
	}

	count := int(order.Uint16(tiff[offset : offset+2]))
	at := offset + 2
	for index := 0; index < count; index++ {
		if at+12 > len(tiff) {
			return None
		}
		entry := tiff[at : at+12]
		at += 12

		if order.Uint16(entry[0:2]) != 0x0112 {
			continue
		}
		// Type 3 is SHORT, which is what orientation is specified as. The
		// value sits in the first two bytes of the value field, because one
		// SHORT is two bytes and anything four bytes or under is stored
		// inline rather than at an offset.
		if order.Uint16(entry[2:4]) != 3 {
			return None
		}
		value := Orientation(order.Uint16(entry[8:10]))
		if !value.Valid() {
			return None
		}
		return value
	}
	return None
}
