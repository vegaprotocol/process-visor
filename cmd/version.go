package cmd

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

var (
	cliVersionHash = ""
	cliVersion     = "develop"
)

var VersionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print software version",
	Long:  `Print software version`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("process-watcher CLI %s (%s)\n", cliVersion, cliVersionHash)
	},
}

func init() {
	info, _ := debug.ReadBuildInfo()

	if info == nil {
		cliVersionHash = "unknown"
		return
	}

	modified := false

	for _, v := range info.Settings {
		if v.Key == "vcs.revision" {
			cliVersionHash = v.Value
		}
		if v.Key == "vcs.modified" && v.Value == "true" {
			modified = true
		}
	}
	if modified {
		cliVersionHash += "-modified"
	}
}
