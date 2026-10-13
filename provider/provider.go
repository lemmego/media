// Package provider wires a [media.Manager] into a lemmego application.
//
// It lives in a subpackage for the reason lemmego/log recorded: api/app
// reaches api/db and therefore a SQL driver, so a root package that offered
// the provider would charge every consumer for the framework in order to
// resize an image. The root imports the standard library and
// golang.org/x/image, and a test asserts it.
package provider

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/lemmego/api/app"
	"github.com/lemmego/fsys"

	"github.com/lemmego/media"
)

// Provider builds a [media.Manager] over one of the application's disks and
// registers it in the container.
type Provider struct {
	// Disk names the filesystem renditions are written to. Empty takes the
	// application's default disk, which is what MEDIA_DISK overrides.
	Disk string

	// StoreFor replaces the disk lookup entirely, for an application whose
	// media lives somewhere its FileSystem does not describe. It is a
	// function rather than a value because providers run before the
	// container is populated — the mistake auth.Opts.SocialStore shipped as
	// a value and had to correct a release later.
	StoreFor func(a app.App) (media.Store, error)

	// These mirror [media.Options] and are overridden by MEDIA_MAX_PIXELS,
	// MEDIA_MAX_BYTES and MEDIA_QUALITY. Zero takes the package default.
	MaxPixels int
	MaxBytes  int64
	Quality   int
}

var (
	_ app.Provider = (*Provider)(nil)

	// The claim the whole Store interface rests on, stated rather than
	// discovered: lemmego/fsys.FS satisfies media.Store with no adapter, so
	// an application hands over the disk it already has and the root package
	// never imports fsys. This assertion belongs here because the provider
	// already depends on fsys through api/app, and the root must not.
	_ media.Store = (fsys.FS)(nil)
)

// Provide registers the manager.
//
// A disk that cannot be resolved fails the boot. The alternative is an
// application that serves upload forms and discovers at the first upload that
// it has nowhere to put the file — and by then somebody has typed a post.
func (p *Provider) Provide(a app.App) error {
	store, err := p.store(a)
	if err != nil {
		return err
	}

	manager, err := media.New(media.Options{
		Store:     store,
		MaxPixels: envInt("MEDIA_MAX_PIXELS", p.MaxPixels),
		MaxBytes:  int64(envInt("MEDIA_MAX_BYTES", int(p.MaxBytes))),
		Quality:   envInt("MEDIA_QUALITY", p.Quality),
	})
	if err != nil {
		return err
	}

	a.AddService(manager)
	return nil
}

func (p *Provider) store(a app.App) (media.Store, error) {
	if p.StoreFor != nil {
		store, err := p.StoreFor(a)
		if err != nil {
			return nil, fmt.Errorf("media: building the store: %w", err)
		}
		if store == nil {
			return nil, errors.New("media: StoreFor returned no store")
		}
		return store, nil
	}

	manager := a.FileSystem()
	if manager == nil {
		return nil, errors.New("media: the application has no FileSystem, so " +
			"there is no disk to write renditions to — register api/providers/fs " +
			"before this provider, or set StoreFor")
	}

	name := p.Disk
	if fromEnv := os.Getenv("MEDIA_DISK"); fromEnv != "" {
		name = fromEnv
	}

	var disk media.Store
	var err error
	if name == "" {
		// fsys.FS satisfies media.Store structurally, which is the whole
		// reason the interface is shaped the way it is: no adapter, and the
		// root package never imports fsys.
		disk, err = manager.Disk()
	} else {
		disk, err = manager.Disk(name)
	}
	if err != nil {
		return nil, fmt.Errorf("media: resolving the %q disk: %w", name, err)
	}
	return disk, nil
}

// envInt reads an integer setting, falling back to the value already given.
//
// Deliberately not config.MustEnv, for the two reasons this project has now
// found three times: MustEnv treats a variable that is *present and empty* as
// a decision, so a stray "MEDIA_QUALITY=" line in a production .env would be
// read as a choice; and it panics on a value it cannot parse, so a typo takes
// the process down before anything can log why.
//
// Absent and empty both mean "use the default" here, and an unparseable value
// means the same plus a complaint.
func envInt(name string, fallback int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		slog.Warn("media: a setting could not be read and the default was used",
			"variable", name, "value", raw, "error", err)
		return fallback
	}
	return value
}
