package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// versionCmd represents the version command
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Display version information",
	Long:  `Display acyl version, commit SHA and build date`,
	Run: func(cmd *cobra.Command, args []string) {
		commit := "https://github.com/bkeroack/acyl/commit/" + Commit
		if Commit == "" {
			commit = "n/a"
		}
		fmt.Printf("version: %v\ncommit: %v\nbuild date: %v\n", Version, commit, Date)
	},
}

func init() {
	RootCmd.AddCommand(versionCmd)
}
