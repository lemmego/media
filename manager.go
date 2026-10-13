package media

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"log/slog"
)

// Defaults, each chosen for a reason rather than rounded.
const (
	// DefaultMaxPixels is the largest image this package will decode: a
	// little over forty megapixels, which covers every phone and most
	// cameras, and is 160MB of RGBA once decoded. The limit exists because
	// the cost of decoding is proportional to the *declared* pixel count and
	// not to the file size, so a few kilobytes of deflate can ask for ten
	// gigabytes.
	DefaultMaxPixels = 40_000_000

	// DefaultMaxBytes is twenty megabytes. An application almost always has
	// a tighter limit of its own at the validation layer; this is the
	// backstop for the callers that do not.
	DefaultMaxBytes = 20 << 20

	// DefaultQuality is JPEG 82, the point either side of which the file
	// size and the visible artefacts both start moving quickly.
	DefaultQuality = 82
)

// Options configures a [Manager].
type Options struct {
	// Store is where renditions are written. Required.
	Store Store

	// MaxPixels refuses a source declaring more pixels than this, from its
	// header and before anything is allocated. Zero takes
	// [DefaultMaxPixels]; negative means no limit, which is a choice to make
	// deliberately and not a default to fall into.
	MaxPixels int

	// MaxBytes refuses a source longer than this. Zero takes
	// [DefaultMaxBytes]; negative means no limit.
	MaxBytes int64

	// Quality is the JPEG quality for conversions that do not set their own.
	// Zero takes [DefaultQuality].
	Quality int
}

// Manager ingests uploads into stored renditions.
//
// Safe for concurrent use: it holds configuration and a Store, and every
// ingest works on its own bytes.
type Manager struct {
	store     Store
	maxPixels int
	maxBytes  int64
	quality   int
}

// New builds a Manager.
func New(opts Options) (*Manager, error) {
	if opts.Store == nil {
		return nil, errors.New("media: New needs a Store — lemmego/fsys.FS satisfies it")
	}

	manager := &Manager{
		store:     opts.Store,
		maxPixels: opts.MaxPixels,
		maxBytes:  opts.MaxBytes,
		quality:   opts.Quality,
	}
	if manager.maxPixels == 0 {
		manager.maxPixels = DefaultMaxPixels
	}
	if manager.maxPixels < 0 {
		manager.maxPixels = int(^uint(0) >> 1) // no limit, as asked
	}
	if manager.maxBytes == 0 {
		manager.maxBytes = DefaultMaxBytes
	}
	if manager.quality == 0 {
		manager.quality = DefaultQuality
	}
	if manager.quality < 1 || manager.quality > 100 {
		return nil, fmt.Errorf("media: a JPEG quality of %d is outside 1–100", manager.quality)
	}
	return manager, nil
}

// OriginalPolicy decides what becomes the primary stored file.
type OriginalPolicy uint8

const (
	// CleanOriginal re-encodes the source, which discards every metadata
	// block. The default, and the only policy that strips EXIF.
	CleanOriginal OriginalPolicy = iota

	// RawOriginal stores the submitted bytes unchanged. It keeps the
	// metadata too, so it is a decision rather than a convenience: a
	// photograph stored this way serves its GPS coordinates to every viewer.
	RawOriginal

	// NoOriginal writes the conversions and nothing else. For an avatar,
	// where no page ever shows the upload at its original size and keeping
	// it is storage spent on nothing.
	NoOriginal
)

// Request describes one ingest.
type Request struct {
	// Dir and Base together name the stored files: a Dir of "posts" and a
	// Base of "9-1760000000-0" writes "posts/9-1760000000-0.jpg" and
	// "posts/9-1760000000-0_sm.jpg".
	//
	// Base carries no extension — the extension follows from the format each
	// rendition was actually encoded in, which for [FormatAuto] is not known
	// until the pixels have been read.
	Dir  string
	Base string

	Original    OriginalPolicy
	Conversions []Conversion
}

// Ingest decodes the upload, derives every requested rendition and stores
// them.
//
// The context bounds the work *between* writes rather than inside them, and
// that limit is the Store's: fsys exposes no context on any method, so a
// write already in flight cannot be cancelled. With several conversions to
// make, checking between them still ends a request whose client has gone.
//
// Nothing partial survives a failure. A conversion that fails after two were
// written has those two deleted before the error returns, because the
// alternative is a store accumulating files no row refers to — invisible
// until somebody measures the bucket.
func (m *Manager) Ingest(ctx context.Context, src io.Reader, req Request) (Result, error) {
	if err := validPathPart("dir", req.Dir); err != nil {
		return Result{}, err
	}
	if err := validPathPart("base", req.Base); err != nil {
		return Result{}, err
	}
	seen := make(map[string]bool, len(req.Conversions))
	for _, conversion := range req.Conversions {
		if err := ValidName(conversion.Name); err != nil {
			return Result{}, err
		}
		if seen[conversion.Name] {
			// Two conversions with one name write to one path, so the second
			// silently replaces the first and the Result reports both.
			return Result{}, fmt.Errorf("%w: %q is named twice",
				ErrBadConversion, conversion.Name)
		}
		seen[conversion.Name] = true
		if conversion.Width <= 0 && conversion.Height <= 0 {
			return Result{}, fmt.Errorf("%w: %q bounds neither width nor height",
				ErrBadConversion, conversion.Name)
		}
		if conversion.Quality != 0 && (conversion.Quality < 1 || conversion.Quality > 100) {
			return Result{}, fmt.Errorf("%w: %q asks for quality %d, outside 1–100",
				ErrBadConversion, conversion.Name, conversion.Quality)
		}
	}

	raw, err := m.readAll(src)
	if err != nil {
		return Result{}, err
	}

	img, source, err := m.decode(raw)
	if err != nil {
		return Result{}, err
	}

	result := Result{Source: source}
	written := make([]string, 0, len(req.Conversions)+1)

	// Undo on any failure from here on.
	fail := func(err error) (Result, error) {
		m.rollback(written)
		return Result{}, err
	}

	if req.Original != NoOriginal {
		original, err := m.writeOriginal(raw, img, source, req)
		if err != nil {
			return fail(err)
		}
		result.Original = original
		result.OriginalRaw = m.rawOriginal(source, req)
		written = append(written, original.Path)
	}

	for _, conversion := range req.Conversions {
		if err := ctx.Err(); err != nil {
			return fail(fmt.Errorf("media: %q was not written: %w", conversion.Name, err))
		}

		scaled := resize(img, conversion)
		quality := conversion.Quality
		if quality == 0 {
			quality = m.quality
		}
		// Re-scanned rather than inherited: scaling resamples, so an edge
		// that was opaque in the source can become translucent and a source
		// with one translucent pixel can lose it to a downscale. Deciding
		// the format from the rendition that is actually being written is
		// the only way the answer is about that rendition.
		data, format, err := encode(scaled, conversion.Format, quality, transparent(scaled))
		if err != nil {
			return fail(err)
		}

		path := storedPath(req.Dir, req.Base, conversion.Name, extensionFor(format))
		if err := m.store.Write(path, data); err != nil {
			return fail(fmt.Errorf("media: writing %q: %w", path, err))
		}
		written = append(written, path)

		bounds := scaled.Bounds()
		result.Variants = append(result.Variants, Variant{
			Name:   conversion.Name,
			Path:   path,
			Width:  bounds.Dx(),
			Height: bounds.Dy(),
			Bytes:  int64(len(data)),
			Format: format,
		})
	}

	return result, nil
}

// rawOriginal reports whether the submitted bytes are what gets stored.
func (m *Manager) rawOriginal(source Source, req Request) bool {
	return req.Original == RawOriginal || source.Animated()
}

// writeOriginal stores the primary file.
func (m *Manager) writeOriginal(raw []byte, img image.Image, source Source, req Request) (Variant, error) {
	if m.rawOriginal(source, req) {
		// An animated GIF takes this path whatever the policy says.
		// Re-encoding it would write the poster frame and throw the
		// animation away — silently, since the result is a valid image of
		// the right size. GIF carries no camera metadata, so preserving the
		// bytes costs nothing that CleanOriginal exists to prevent, and the
		// Result reports it as raw either way.
		path := storedPath(req.Dir, req.Base, "", extensionFor(source.Format))
		if err := m.store.Write(path, raw); err != nil {
			return Variant{}, fmt.Errorf("media: writing %q: %w", path, err)
		}
		return Variant{
			Path:   path,
			Width:  source.Width,
			Height: source.Height,
			Bytes:  int64(len(raw)),
			Format: source.Format,
		}, nil
	}

	format := FormatAuto
	data, encoded, err := encode(img, format, m.quality, source.Transparent)
	if err != nil {
		return Variant{}, err
	}
	path := storedPath(req.Dir, req.Base, "", extensionFor(encoded))
	if err := m.store.Write(path, data); err != nil {
		return Variant{}, fmt.Errorf("media: writing %q: %w", path, err)
	}
	return Variant{
		Path:   path,
		Width:  source.Width,
		Height: source.Height,
		Bytes:  int64(len(data)),
		Format: encoded,
	}, nil
}

// Remove deletes stored renditions, reporting every failure rather than the
// first.
//
// A missing file is not a failure: the common caller is cleaning up after a
// transaction that rolled back, and whether a particular write landed is
// exactly what it cannot know.
func (m *Manager) Remove(paths ...string) error {
	var errs []error
	for _, path := range paths {
		if path == "" {
			continue
		}
		if err := m.store.Delete(path); err != nil {
			if exists, checkErr := m.store.Exists(path); checkErr == nil && !exists {
				continue
			}
			errs = append(errs, fmt.Errorf("media: deleting %q: %w", path, err))
		}
	}
	return errors.Join(errs...)
}

// rollback removes what a failed ingest had already written.
func (m *Manager) rollback(paths []string) {
	for _, path := range paths {
		if err := m.store.Delete(path); err != nil {
			// Logged rather than returned: the caller is already receiving
			// the error that caused this, and replacing it with a cleanup
			// failure would hide the reason the ingest failed.
			slog.Warn("media: a partial rendition could not be removed",
				"path", path, "error", err)
		}
	}
}

// readAll reads the upload, refusing one over the limit without buffering it
// all first.
func (m *Manager) readAll(src io.Reader) ([]byte, error) {
	if m.maxBytes < 0 {
		return io.ReadAll(src)
	}
	// One byte past the limit, so an upload exactly at it is accepted and a
	// longer one is detected without reading the rest.
	raw, err := io.ReadAll(io.LimitReader(src, m.maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("media: reading the upload: %w", err)
	}
	if int64(len(raw)) > m.maxBytes {
		return nil, fmt.Errorf("%w: over %d bytes", ErrTooManyBytes, m.maxBytes)
	}
	return raw, nil
}
