// Package buildinfo identifies the application release independently of schema versions.
package buildinfo

// Version is set by release builds with -ldflags; source builds use this default.
var Version = "0.3.1-beta.1"
