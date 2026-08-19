// Command revctl is the Reversibility Engine command-line interface.
//
// It analyzes a changeset of PostgreSQL migrations and rendered Kubernetes manifests and emits
// a reversibility certificate, exiting non-zero when the requested grade is not met.
//
// The command tree lives in internal/delivery/cli. This file stays a launcher so that transport
// remains replaceable and everything above it stays testable without ending the process.
package main

import (
	"os"

	"github.com/abdo-s1/reversibility-engine/internal/delivery/cli"
)

func main() {
	os.Exit(cli.Execute(cli.Options{
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Args:   os.Args[1:],
	}))
}
