package main

import (
	"github.com/spf13/cobra"
	"github.com/vegaprotocol/process-watcher/cmd"
)

var (
	configFilePath string

	rootCmd = &cobra.Command{
		Use:   "process-watcher",
		Short: "A command used to supervise service",
	}
)

func init() {
	rootCmd.PersistentFlags().StringVarP(
		&configFilePath,
		"config-path",
		"c",
		"./config.toml",
		"Path to the config file",
	)
}

func main() {
	rootCmd.AddCommand(cmd.RunCmd)
	rootCmd.AddCommand(cmd.VersionCmd)

	if err := rootCmd.Execute(); err != nil {
		panic(err)
	}
}
