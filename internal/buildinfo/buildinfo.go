// Package buildinfo exposes build metadata for runtime diagnostics.
package buildinfo

// Version is overridden during release builds with -ldflags "-X alarmengine/internal/buildinfo.Version=<version>".
var Version = "v0.0.0"
