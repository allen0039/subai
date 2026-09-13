// Package buildinfo exposes the identity embedded in a SubAI server binary.
package buildinfo

// These defaults keep development binaries useful. Release builds override
// them with -ldflags so the value reported by the running server identifies
// the image itself, rather than the browser bundle that happens to be open.
var (
	Version  = "dev"
	Revision = "unknown"
	BuiltAt  = "unknown"
)

// Info is the public, non-sensitive build identity returned by the server.
type Info struct {
	Version  string `json:"version"`
	Revision string `json:"revision"`
	BuiltAt  string `json:"built_at"`
}

// Current returns the build identity of the running process.
func Current() Info {
	return Info{Version: Version, Revision: Revision, BuiltAt: BuiltAt}
}
