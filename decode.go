package media

import (
	"bytes"
	"fmt"
	"image"

	// The decoders. Registered for their side effect, which is what
	// image.Decode dispatches on.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	// WebP decodes and does not encode: x/image ships no WebP encoder and
	// every pure-Go one is either incomplete or a CGO binding. So a WebP
	// upload is accepted and its renditions are written as JPEG or PNG,
	// which is a real limitation and is in the README rather than discovered
	// from a file extension.
	_ "golang.org/x/image/webp"

	"github.com/lemmego/media/exif"
)

// decode reads the bytes into an image, refusing anything too large to decode
// safely, and reports what it found.
//
// The order matters and is the whole of the safety argument. image.DecodeConfig
// parses only the header, so the pixel count is known before a single pixel is
// allocated — and a 50,000 × 50,000 PNG is a few kilobytes of deflate that
// expands to ten gigabytes of RGBA. Checking the size after decoding is
// checking it after the damage.
func (m *Manager) decode(raw []byte) (image.Image, Source, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, Source{}, fmt.Errorf("%w: %v", ErrNotAnImage, err)
	}

	// int64 deliberately: two 32-bit dimensions multiply past MaxInt32, so
	// the guard itself would overflow into a small positive number and admit
	// exactly the image it exists to refuse.
	if int64(config.Width)*int64(config.Height) > int64(m.maxPixels) {
		return nil, Source{}, pixelError(config.Width, config.Height, m.maxPixels)
	}

	source := Source{
		Format:      format,
		Bytes:       int64(len(raw)),
		Orientation: int(exif.None),
		Frames:      1,
	}

	if format == "gif" {
		frames, err := countGIFFrames(raw)
		if err != nil {
			return nil, Source{}, fmt.Errorf("%w: %v", ErrNotAnImage, err)
		}
		source.Frames = frames
	}

	// image.Decode on a GIF reads the first frame only, which is the poster
	// frame and exactly what a still rendition should show.
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, Source{}, fmt.Errorf("%w: %v", ErrNotAnImage, err)
	}

	// Orientation is read from JPEG alone. PNG can carry an eXIf chunk and
	// WebP an EXIF block, but neither is produced by a camera, and a reader
	// for a case that does not occur is a reader nothing tests.
	if format == "jpeg" {
		orientation := exif.FromJPEG(raw)
		source.Orientation = int(orientation)
		img = applyOrientation(img, orientation)
	}

	bounds := img.Bounds()
	source.Width, source.Height = bounds.Dx(), bounds.Dy()
	source.Transparent = transparent(img)

	return img, source, nil
}
