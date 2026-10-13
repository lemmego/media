package media_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestTheRootIsTheStandardLibraryPlusXImage pins this package's dependency
// set, because the whole shape of the module depends on it.
//
// The lesson is lemmego/log's: with its provider inside the module, a
// consumer importing only the core inherited 289 modules — cloud SDKs,
// OpenTelemetry, gRPC — for a package that formats text. Moving the provider
// out took it to 2.
//
// Here the specific temptation is lemmego/fsys. Writing files is what this
// package does, so declaring [media.Store] as fsys.FS reads as the obvious
// design — and fsys offers S3 and GCS drivers from the same package, so that
// one line charges every consumer for the AWS and Google Cloud SDKs. The
// four-method interface exists to avoid exactly that, and fsys.FS satisfies
// it structurally, so nothing is lost but the import.
//
// golang.org/x/image is the one permitted exception: it supplies the
// resampling kernels and the WebP decoder, and brings nothing with it.
func TestTheRootIsTheStandardLibraryPlusXImage(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("listing dependencies: %v", err)
	}

	for _, dep := range strings.Fields(string(out)) {
		switch {
		case dep == "github.com/lemmego/media",
			strings.HasPrefix(dep, "github.com/lemmego/media/"),
			strings.HasPrefix(dep, "golang.org/x/image"):
			continue
		}
		// A standard-library path has no dot in its first segment, which is
		// how the toolchain itself tells the two apart.
		first, _, _ := strings.Cut(dep, "/")
		if strings.Contains(first, ".") {
			t.Errorf("the root package depends on %s.\n"+
				"Only the standard library and golang.org/x/image belong here. "+
				"If this is lemmego/fsys, it is the import the Store interface "+
				"exists to avoid: fsys serves S3 and GCS from the same package, "+
				"so depending on it charges every consumer for two cloud SDKs. "+
				"If it is api/app, it belongs in media/provider.", dep)
		}
	}
}

// And the provider is where the framework dependency is allowed to be, so
// this asserts the split is real rather than intended.
func TestTheProviderIsWhereTheFrameworkDependencyLives(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "./provider").Output()
	if err != nil {
		t.Fatalf("listing the provider's dependencies: %v", err)
	}
	if !strings.Contains(string(out), "github.com/lemmego/api/app") {
		t.Error("media/provider does not depend on api/app, so either the " +
			"provider is not wired to the container or this test is in the " +
			"wrong place")
	}
}
