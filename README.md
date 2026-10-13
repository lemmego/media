# lemmego/media

Turns an uploaded image into the renditions a page actually serves: a cleaned
original, a set of sized variants, and their real dimensions reported back so
markup can reserve the space before the bytes arrive.

```go
manager, err := media.New(media.Options{Store: app.FileSystem().Disk("public")})

result, err := manager.Ingest(ctx, upload, media.Request{
    Dir:  "posts",
    Base: "9-1760000000-0",
    Conversions: []media.Conversion{
        {Name: "sm", Width: 400, Height: 400},
        {Name: "md", Width: 1000, Height: 1000},
    },
})

// posts/9-1760000000-0.jpg      the original, re-encoded, no metadata
// posts/9-1760000000-0_sm.jpg   400×267
// posts/9-1760000000-0_md.jpg   1000×667
```

## Pure Go, and why that is a constraint rather than a preference

spatie's image-optimizer shells out to `jpegoptim`, `pngquant`, `optipng` and
`svgo`. An application deployed as a single binary either carries four host
packages or silently optimises nothing — and silently is the problem: the
no-op looks exactly like success.

So there is no CGO and no subprocess here. Optimising means **re-encoding at a
chosen quality and discarding every metadata block**, which is most of the
win: a camera JPEG at quality 95 with its EXIF and embedded thumbnail stripped
is routinely half the size at quality 82 with no visible difference.

The dependency is the standard library plus `golang.org/x/image`, for the
resampling kernels and the WebP decoder. A test asserts it.

## Metadata, and the trap under it

Re-encoding discards EXIF, which it has to: a photograph taken on a phone
carries **GPS coordinates**, a device serial number and a timestamp, and
serving the submitted bytes back unchanged publishes all three to everybody
who can see the page.

But stripping EXIF is not safe on its own. A phone does not rotate the pixels
it writes — it stores the sensor's landscape frame and adds a tag asking the
viewer for a quarter turn. `image/jpeg` ignores that tag, and so does every
browser once it is gone. **So discarding metadata without first applying
orientation turns every portrait photograph on its side**: a privacy fix that
visibly breaks the feed.

`media/exif` therefore reads exactly one field, the pixels are turned, and
then re-encoding throws the rest away. It is deliberately not a general EXIF
library.

## Nothing is upscaled

A 32-pixel avatar asked for a 256-pixel variant stays 32 pixels. Enlarging
adds bytes and no detail.

Which is why **a caller must read the dimensions in the result rather than
assume the ones it asked for**. `FitCover` applies the same rule to the box:
a 40×40 avatar slot given a 30×50 photograph yields 30×30 — the requested
shape, at the size the source can supply.

## The format is chosen from the pixels

`FormatAuto` writes PNG when the source has a pixel that is genuinely
translucent, and JPEG when it does not.

The distinction is not pedantic. Most PNGs on the web are RGBA with every
pixel opaque — a screenshot saved by a tool that always writes an alpha
channel. Choosing by colour model keeps those as PNG, several times larger
than the JPEG they could be.

It is also re-decided per variant, not inherited from the source: scaling
resamples, so an edge that was opaque can become translucent and a lone
translucent pixel can be lost to a downscale.

Asking for `FormatJPEG` from a transparent source is allowed, and the image is
composited **onto white** first. JPEG has no alpha channel and the encoder
reads the colour values as given — so a transparent pixel, stored as
transparent *black*, encodes as black, and a logo arrives on a black
rectangle.

## WebP decodes and does not encode

`x/image` ships no WebP encoder, and every pure-Go one is either incomplete or
a CGO binding. A WebP upload is accepted and its renditions are written as
JPEG or PNG.

## Animated GIFs keep their animation

Re-encoding one writes its poster frame and throws the animation away —
silently, because the result is a valid image of the right size. So the
submitted bytes are preserved for an animated GIF whatever `Original` says,
and `Result.OriginalRaw` reports it. The variants are stills, which is the
honest rendition of an animation in a thumbnail slot.

Detecting animation is why there is a GIF block walker in here. The only
function in the standard library that reports a frame count is
`gif.DecodeAll`, which holds **every frame in memory at once** — so a few
megabytes of LZW, well inside any sane upload limit, expands to gigabytes of
`*image.Paletted`. Walking the block structure reads the count and
decompresses nothing.

## The decode bomb

`image.DecodeConfig` parses only the header, so the pixel count is known
before anything is allocated — and a 50,000 × 50,000 PNG is a few kilobytes of
deflate that asks for ten gigabytes of RGBA. Checking the size after decoding
is checking it after the damage.

`MaxPixels` defaults to forty megapixels, which covers every phone and most
cameras. The guard multiplies as `int64` on purpose: two 32-bit dimensions
multiply past `MaxInt32`, so a narrower guard overflows into a small positive
number and admits the one image it exists to refuse.

## Nothing partial survives a failure

A conversion that fails after two were written has those two deleted before
the error returns. Orphaned files are invisible until somebody measures the
bucket.

One level up, that is the caller's job and `Result.Paths()` is for it: a post
whose row fails to insert has already had its images stored, so hand those
paths to `Manager.Remove`, which treats a file that was never written as
success — whether a particular write landed is exactly what a rollback cannot
know.

## The store seam

```go
type Store interface {
    Write(path string, contents []byte) error
    Read(path string) (io.ReadCloser, error)
    Delete(path string) error
    Exists(path string) (bool, error)
}
```

`lemmego/fsys.FS` satisfies this structurally, so an application passes the
handle it already has and **this package never imports `fsys`**. That is not
tidiness: `fsys` serves S3 and GCS from the same package, so depending on it
would charge every consumer for the AWS and Google Cloud SDKs — the
289-module import `lemmego/log` was built to undo.

The split is measured rather than intended. The root **package** builds
`golang.org/x/image` and nothing else; `media/provider`, which needs the
container, reaches 147 modules. Both are in one module, so `go.mod` lists
`api` either way — graph pruning, not the package split, is what keeps a
consumer that imports only the root from building any of it. That is the
convention `cache` and `notify` already follow.

`Write` takes a `[]byte` rather than an `io.Reader` because `fsys` has no
streaming write: its own `Write` takes a slice, and the local driver
`io.ReadAll`s the one `io.Reader`-shaped entry point it offers. Promising a
stream here would be promising something no driver underneath can keep.

## Paths

`dir/base.ext` for the original and `dir/base_name.ext` for a variant, so a
caller that pinned the format can build a URL without reading a row back —
`VariantPath` is that derivation.

It is unpredictable for exactly one case and says so with a second return
value: `FormatAuto` chooses between PNG and JPEG by scanning the source, so
the extension is not known until the pixels have been read. Pin a format on
any conversion whose URL you intend to construct rather than store.

## The context bounds the work between writes

`fsys` exposes no context on any method, so a write already in flight cannot
be cancelled. With several conversions to make, checking between them still
ends a request whose client has gone — and that is all `ctx` promises here.

## Wiring

```go
// bootstrap/providers.go
&mediaprovider.Provider{Disk: "public"},
```

`MEDIA_DISK`, `MEDIA_MAX_PIXELS`, `MEDIA_MAX_BYTES` and `MEDIA_QUALITY`
override it. They are read directly rather than through `config.MustEnv`,
which treats a variable that is *present and empty* as a decision and panics
on a value it cannot parse — so a stray `MEDIA_QUALITY=` line would be a
choice and a typo would take the process down before anything could log why.
Absent and empty both mean "use the default", and an unreadable value means
the same plus a complaint.

A disk that cannot be resolved **fails the boot**, rather than letting an
application serve upload forms and discover at the first upload that it has
nowhere to put the file.

## Testing

`media/mediatest` has an in-memory store, a contract suite any `Store` must
pass, and the fixtures that make the interesting cases testable at all: a JPEG
carrying an orientation tag, a PNG whose alpha channel is present but unused,
an animated GIF, and a decode bomb.

The bomb fixture is worth a note, because the first version of it was wrong:
`image/png` verifies the IHDR checksum in `DecodeConfig`, so a rewritten width
without a recomputed CRC is rejected as a corrupt file and never reaches the
pixel guard. The guard cannot be tested with a damaged header — only with a
well-formed lie.

```bash
go test ./...
GOWORK=off go mod tidy && GOWORK=off go test ./...
```

## Not in v0

- **WebP and AVIF output.** No pure-Go encoder for either is ready.
- **Video.** Thumbnailing one means a demuxer and a decoder, which is a module.
- **Presigned uploads.** `fsys` has no presigned PUT at all, so this would
  start by building one.
- **Deferred conversion.** Variants are made on the request path, inside the
  limits the validator has already imposed, because a variant that arrives
  later means every render site needs a fallback for the window where it has
  not — which is the state an application is in before it adopts this.
- **A focal point for cropping.** `FitCover` centres. Face detection is not a
  pure-Go dependency.
