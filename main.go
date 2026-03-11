package main

import (
	_ "embed"
	"fmt"
	"os"
	"strings"

	"github.com/starkware-libs/merge-gatekeeper/internal/cli"
)

var (
	//go:embed version.txt
	version string
)

func main() {
	if err := cli.Run(strings.TrimSuffix(version, "\n"), os.Args...); err != nil {
		fmt.Fprintf(os.Stderr, "failed to execute command: %v", err)
		os.Exit(1)
	}
}
