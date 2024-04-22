package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/vegaprotocol/process-visor/config"
	"github.com/vegaprotocol/process-visor/service"
	"go.uber.org/zap"
)

var (
	configFilePath string

	rootCmd = &cobra.Command{
		Use:   "process-visor",
		Short: "A command used to supervise service",
		Run: func(cmd *cobra.Command, args []string) {
			config, err := config.ReadFromFile(configFilePath)
			if err != nil {
				panic(err)
			}

			if err := execute(config); err != nil {
				panic(err)
			}
		},
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

func execute(config *config.Config) error {
	programContext, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger, err := zap.NewProduction()
	if err != nil {
		return fmt.Errorf("failed to create logger: %w", err)
	}
	defer func() {
		if err := logger.Sync(); err != nil {
			fmt.Printf("failed to sync logger buffer: %s", err.Error())
		}
	}()

	failureNotifier := make(chan service.ServiceFailureType)

	logStreamWatcher, err := service.NewLogStreamWatcher(config.Commands.LogStream, failureNotifier)
	if err != nil {
		return fmt.Errorf("failed to create new log stream process: %w", err)
	}

	logsFailureDetector := service.NewKeyWordMatcher(config.LogsWatcher.FailureKeywords)
	if err := logStreamWatcher.Start(programContext, logger, logsFailureDetector); err != nil {
		return fmt.Errorf("failed to start log stream process")
	}

	processWatcher, err := service.NewProcessWatcher(failureNotifier)
	if err != nil {
		return fmt.Errorf("failed to create process watcher: %w", err)
	}

	if err := processWatcher.Start(programContext, logger.Named("process-watcher"), &config.ProcessWatcher); err != nil {
		return fmt.Errorf("failed to start process watcher: %w", err)
	}

	serviceManager, err := service.NewServiceManager(
		failureNotifier,
		config.Commands.Stop,
		config.Commands.Start,
	)
	if err != nil {
		return fmt.Errorf("failed to create service manager")
	}
	if err := serviceManager.Start(programContext, logger.Named("service-manager")); err != nil {
		return fmt.Errorf("failed to start service manager : %w", err)
	}

	<-programContext.Done()
	return nil
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		panic(err)
	}
}
