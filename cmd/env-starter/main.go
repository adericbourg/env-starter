// Command env-starter is a text-based meta-launcher that starts a named
// environment's commands in dependency order, waiting for each dependency to be
// healthy before starting its dependents.
package main

import "fmt"

// version is the build version, overridden at release time via -ldflags
// "-X main.version=...".
var version = "dev"

func main() {
	fmt.Printf("env-starter %s\n", version)
}
