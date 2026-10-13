package media

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
)

// encode writes the image out, choosing a format when asked to.
//
// Every path here re-encodes from decoded pixels, which is how metadata is
// discarded: there is no copy step that could carry an EXIF block across. A
// photograph taken on a phone arrives with GPS coordinates, a device serial
// number and a timestamp in it, and serving those bytes back unchanged
// publishes all three to everybody who can see the page. Re-encoding is also
// the whole of the "optimisation" this package offers, and it is most of the
// win: a camera JPEG at quality 95 with its thumbnail and metadata stripped
// is routinely half the size at quality 82 with no visible difference.
func encode(img image.Image, format Format, quality int, hasAlpha bool) ([]byte, string, error) {
	chosen := format
	if chosen == FormatAuto {
		// PNG only when there is something for the alpha channel to carry.
		// See the note on FormatAuto: the test is a real scan for a
		// translucent pixel, not the colour model.
		if hasAlpha {
			chosen = FormatPNG
		} else {
			chosen = FormatJPEG
		}
	}

	var out bytes.Buffer
	switch chosen {
	case FormatPNG:
		encoder := png.Encoder{CompressionLevel: png.BestCompression}
		if err := encoder.Encode(&out, img); err != nil {
			return nil, "", fmt.Errorf("media: encoding PNG: %w", err)
		}
		return out.Bytes(), "png", nil

	case FormatJPEG:
		subject := img
		if hasAlpha {
			// JPEG has no alpha channel and the encoder reads the colour
			// values as given, so a transparent pixel — stored as
			// transparent black — is written as black. Asking for JPEG from
			// a transparent source is a legitimate choice; silently
			// returning a logo on a black rectangle is not.
			subject = flatten(img)
		}
		if err := jpeg.Encode(&out, subject, &jpeg.Options{Quality: quality}); err != nil {
			return nil, "", fmt.Errorf("media: encoding JPEG: %w", err)
		}
		return out.Bytes(), "jpeg", nil
	}
	return nil, "", fmt.Errorf("media: %d is not an encodable format", format)
}

// extensionFor is the filename suffix for an encoded format.
func extensionFor(format string) string {
	switch format {
	case "jpeg":
		return ".jpg"
	case "png":
		return ".png"
	case "gif":
		return ".gif"
	case "webp":
		return ".webp"
	default:
		return ""
	}
}
