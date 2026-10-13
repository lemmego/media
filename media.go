// Package media turns an uploaded image into the renditions a page actually
// serves: a cleaned original and a set of sized variants, with their real
// dimensions reported back so markup can reserve the space before the bytes
// arrive.
//
// It is pure Go. No CGO and no external binaries, which is a constraint
// rather than a preference: spatie's image-optimizer shells out to jpegoptim,
// pngquant, optipng and svgo, and an application deployed as a single binary
// either carries four host packages or silently optimises nothing. Optimising
// here means re-encoding at a chosen quality and discarding every metadata
// block, which is most of the win and costs no installation.
//
// The dependency list is the standard library plus golang.org/x/image, which
// supplies the resampling kernels and the WebP decoder and brings nothing
// else with it.
//
// # The store seam
//
// This package writes through a four-method [Store] and never learns what a
// filesystem is. lemmego/fsys.FS satisfies it structurally, so an application
// passes the handle it already has:
//
//	manager, err := media.New(media.Options{Store: app.FS()})
//
// Declaring the dependency the other way round would charge every consumer
// for the AWS and Google Cloud SDKs that fsys links in order to offer its S3
// and GCS drivers — the 289-module import that lemmego/log was built to
// undo.
package media

import (
	"errors"
	"fmt"
	"io"
)

// Store is the part of a filesystem this package uses.
//
// Four methods, matching lemmego/fsys.FS exactly so that type satisfies this
// one without an adapter. Write takes a []byte rather than an io.Reader
// because fsys has no streaming write — its own Write takes a slice, and the
// local driver io.ReadAll's the one io.Reader-shaped entry point it offers.
// Promising a stream here would be promising something no driver underneath
// can keep.
type Store interface {
	Write(path string, contents []byte) error
	Read(path string) (io.ReadCloser, error)
	Delete(path string) error

	// Exists is here for one reason, and it is [Manager.Remove]: the usual
	// caller is undoing an ingest after a transaction rolled back, and
	// whether a particular write landed is precisely what it cannot know. A
	// delete of a file that was never written must not be reported as a
	// failure, and os.Remove returns an error for it.
	Exists(path string) (bool, error)
}

// Fit decides what happens when the source and the target box disagree about
// their aspect ratio.
type Fit uint8

const (
	// FitInside scales the image to sit entirely within the box, preserving
	// its aspect ratio, so one dimension is usually smaller than asked for.
	// Nothing is cropped. The default, because cropping somebody's
	// photograph is a decision and this is the one that loses no content.
	FitInside Fit = iota

	// FitCover fills the box exactly and centre-crops the overflow. For an
	// avatar slot, which is a fixed square the design has already committed
	// to.
	FitCover
)

// Format is the encoding a variant is written in.
type Format uint8

const (
	// FormatAuto writes PNG when the source actually has a non-opaque pixel
	// and JPEG when it does not.
	//
	// "Actually" is the whole point. Most PNGs are RGBA with every pixel
	// opaque, so choosing by colour model alone keeps them as PNG and a
	// photograph saved from a screenshot tool stays several times larger
	// than it needs to be. This scans for a pixel that is genuinely
	// translucent.
	FormatAuto Format = iota
	FormatJPEG
	FormatPNG
)

func (f Format) String() string {
	switch f {
	case FormatJPEG:
		return "jpeg"
	case FormatPNG:
		return "png"
	default:
		return "auto"
	}
}

// Conversion is one rendition to derive.
type Conversion struct {
	// Name appears in the stored path and is how a caller finds the result:
	// a base of "9-17600" and a Name of "sm" is written as "9-17600_sm.jpg".
	// Required, and refused if it would change the path's shape — see
	// [ValidName].
	Name string

	// Width and Height bound the result. At least one must be positive; a
	// zero means "whatever preserving the aspect ratio gives".
	Width  int
	Height int

	Fit    Fit
	Format Format

	// Quality for JPEG, 1 to 100. Zero takes the manager's default.
	Quality int
}

// Variant is one stored rendition.
type Variant struct {
	// Name is "" for the original and the Conversion's name otherwise.
	Name string

	// Path is where it was written, relative to the store's root, and is
	// derived from the request rather than invented — so a caller that knows
	// the base and the conversion name can build this without a database
	// read. [VariantPath] is that derivation, exported for exactly that.
	Path string

	Width  int
	Height int
	Bytes  int64
	Format string
}

// Source describes what arrived, after decoding and before anything derived
// from it was written.
type Source struct {
	// Format as the decoders name it: jpeg, png, gif or webp.
	Format string

	// Width and Height are the dimensions as a viewer should see them, which
	// is to say after the EXIF orientation was applied. A portrait photograph
	// from a phone is stored landscape with a tag asking for a quarter turn,
	// so these are the turned numbers and not the ones in the file header.
	Width  int
	Height int

	Bytes int64

	// Orientation is the EXIF value found in the source, 1 when there was
	// none. Reported because a 6 or an 8 here is the difference between a
	// photograph the right way up and one on its side.
	Orientation int

	// Frames is 1 for a still image and the frame count for an animated GIF.
	// Counted by walking the GIF's block structure rather than by decoding,
	// because gif.DecodeAll holds every frame in memory at once and a few
	// megabytes of LZW can expand to several gigabytes of *image.Paletted.
	Frames int

	// Transparent reports whether a pixel was found that is not fully
	// opaque, not merely whether the colour model has an alpha channel.
	Transparent bool
}

// Animated reports whether the source carries more than one frame.
func (s Source) Animated() bool { return s.Frames > 1 }

// Result is what one ingest produced.
type Result struct {
	// Original is the cleaned source: re-encoded with no metadata, or — for
	// an animated GIF, where re-encoding would silently drop the animation —
	// the submitted bytes unchanged.
	Original Variant

	// OriginalRaw reports that [Result.Original] holds the submitted bytes
	// rather than a re-encoded copy — so its metadata is intact. True when
	// the caller asked for [RawOriginal], and true for an animated GIF
	// whatever was asked, because re-encoding one writes its poster frame
	// and discards the animation.
	OriginalRaw bool

	// Variants are the derived renditions, in the order they were requested.
	Variants []Variant

	Source Source
}

// Variant returns the named rendition.
func (r Result) Variant(name string) (Variant, bool) {
	for _, variant := range r.Variants {
		if variant.Name == name {
			return variant, true
		}
	}
	return Variant{}, false
}

// Paths returns every path this ingest wrote, original first.
//
// Which is what a caller needs to undo it: a post whose row failed to insert
// has already had its images stored, and this is the list to hand to
// [Manager.Remove].
func (r Result) Paths() []string {
	paths := make([]string, 0, len(r.Variants)+1)
	if r.Original.Path != "" {
		paths = append(paths, r.Original.Path)
	}
	for _, variant := range r.Variants {
		paths = append(paths, variant.Path)
	}
	return paths
}

// Errors this package returns. Each is wrapped with the detail that makes it
// actionable, so compare with errors.Is and print the error itself.
var (
	// ErrNotAnImage means no decoder recognised the bytes.
	ErrNotAnImage = errors.New("media: the bytes are not a supported image")

	// ErrTooManyPixels means the header declares more pixels than the
	// manager will decode. Checked from the header, before any allocation —
	// which is the point of it. See [Options.MaxPixels].
	ErrTooManyPixels = errors.New("media: the image has more pixels than the limit allows")

	// ErrTooManyBytes means the upload exceeds [Options.MaxBytes].
	ErrTooManyBytes = errors.New("media: the image is larger than the byte limit")

	// ErrBadConversion means a Conversion could not be used as written.
	ErrBadConversion = errors.New("media: the conversion is not usable")

	// ErrBadRequest means the Dir or Base would not produce a usable path.
	ErrBadRequest = errors.New("media: the request does not name a usable path")
)

// pixelError builds the ErrTooManyPixels message with both numbers in it,
// because "too large" without the limit sends the reader to the source.
func pixelError(width, height, limit int) error {
	return fmt.Errorf("%w: %d×%d is %d pixels against a limit of %d",
		ErrTooManyPixels, width, height, width*height, limit)
}
