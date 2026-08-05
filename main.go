package main

import "github.com/bkeroack/acyl/cmd"

// Set by goreleaser at release build time
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func init() {
	cmd.Version = version
	cmd.Commit = commit
	cmd.Date = date
}

func main() {
	cmd.Execute()
}
