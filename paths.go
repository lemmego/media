package media

import (
	"fmt"
	"path"
	"strings"
)

// ValidName reports whether a conversion name can appear in a stored path.
//
// The name is interpolated into a filename, so anything that could change the
// path's shape is refused rather than sanitised: a silently rewritten name
// means the path a caller predicts and the path this package writes disagree,
// which is a broken image with nothing logged.
func ValidName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: the name is empty", ErrBadConversion)
	}
	if strings.ContainsAny(name, "/\\.") {
		return fmt.Errorf("%w: %q contains a path separator or a dot", ErrBadConversion, name)
	}
	for _, r := range name {
		// Letters, digits, hyphen and underscore. Deliberately narrow: a
		// space or a non-ASCII character survives a local filesystem and
		// then needs escaping in a URL, which is a bug that appears only
		// once the file is served.
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_':
		default:
			return fmt.Errorf("%w: %q contains %q, which is not a letter, digit, hyphen or underscore",
				ErrBadConversion, name, r)
		}
	}
	return nil
}

// validPathPart refuses a directory or base that would escape the store.
func validPathPart(label, value string) error {
	if value == "" {
		return fmt.Errorf("%w: the %s is empty", ErrBadRequest, label)
	}
	if strings.HasPrefix(value, "/") {
		return fmt.Errorf("%w: the %s %q is absolute", ErrBadRequest, label, value)
	}
	if value != path.Clean(value) {
		return fmt.Errorf("%w: the %s %q is not already clean, so what it names "+
			"depends on who resolves it", ErrBadRequest, label, value)
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return fmt.Errorf("%w: the %s %q climbs out of the store", ErrBadRequest, label, value)
		}
	}
	return nil
}

// VariantPath returns where a variant is written, and whether that is
// predictable.
//
// The derivation is deliberately plain — `dir/base_name.ext`, with the
// original as `dir/base.ext` — so a caller that pinned the format can build a
// URL without reading a row back.
//
// It is unpredictable for exactly one case, and the second return value says
// so rather than guessing: [FormatAuto] chooses between PNG and JPEG by
// scanning the source's pixels, so the extension is not known until the image
// has been decoded. Pin FormatJPEG or FormatPNG on any conversion whose URL
// you intend to construct rather than store.
func VariantPath(dir, base, name string, format Format) (string, bool) {
	extension := extensionFor(format.String())
	if extension == "" {
		return "", false
	}
	return storedPath(dir, base, name, extension), true
}

func storedPath(dir, base, name, extension string) string {
	if name == "" {
		return path.Join(dir, base+extension)
	}
	return path.Join(dir, base+"_"+name+extension)
}
