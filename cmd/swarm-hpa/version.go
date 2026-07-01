package main

import (
	"fmt"
	"runtime"
)

// versionString returns a single-line, script-friendly version banner:
//
//	swarm-hpa <version> (go <goversion>, <os>/<arch>)
//
// version is the build-time-injected main.version (defaults to "dev").
func versionString() string {
	return fmt.Sprintf("swarm-hpa %s (%s, %s/%s)", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// wantsVersion reports whether the CLI args request the version banner
// (--version / -v). It is checked before config parsing so the flag works
// without a Docker socket (e.g. `docker run <image> --version`).
func wantsVersion(args []string) bool {
	for _, a := range args {
		switch a {
		case "--version", "-version", "-v", "--v":
			return true
		}
	}
	return false
}
