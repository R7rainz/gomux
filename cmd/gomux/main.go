package main

import (
	"flag"
	"fmt"
	"os"
	"runtime/debug"
)

// version is set by release builds: go build -ldflags "-X main.version=v1.2.3".
var version string

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Usage = func() { fmt.Fprintln(os.Stderr, "usage: gomux --version") }
	flag.Parse()
	if !*showVersion || flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}
	fmt.Println("gomux", versionString())
}

// versionString prefers the release-stamped version, then the module version
// Go embeds (go install …@vX, or git-derived for local builds), then "dev".
func versionString() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
