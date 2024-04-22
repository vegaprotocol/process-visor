package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/vegaprotocol/pyth-service-monitor/clients/docker"
	"github.com/vegaprotocol/pyth-service-monitor/config"
	"github.com/vegaprotocol/pyth-service-monitor/internal/tools"
	"go.uber.org/zap"
)

type FailureOnType string

const (
	LogWatcherSubscriptionFailure FailureOnType = "log-watcher-subscription-failure"
	StdoutFailure                 FailureOnType = "stdout"
	StderrFailure                 FailureOnType = "stderr"
	ProcessWatcherFailure         FailureOnType = "process-watcher"
	ProcessWatcherInternalError   FailureOnType = "process-watcher-internal-error"
)

const (
	RestartEverySeconds = 60 // We won't restart more often than time defined here
)

var (
	configFilePath string

	rootCmd = &cobra.Command{
		Use:   "pyth-service-monitor",
		Short: "A command used to analyze and restart pyth-price-pusher",
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

type LogStreamWatcher struct {
	cmd []string

	failureNotifier chan<- FailureOnType
}

func NewLogStreamWatcher(logsStreamCommand []string, notifier chan<- FailureOnType) (*LogStreamWatcher, error) {
	if len(logsStreamCommand) < 1 {
		return nil, fmt.Errorf("invalid command for -logs-stream-command")
	}

	return &LogStreamWatcher{
		cmd:             logsStreamCommand,
		failureNotifier: notifier,
	}, nil
}

func (lsw *LogStreamWatcher) Start(
	ctx context.Context,
	logger *zap.Logger,
	logLineAnalyzer func(string) bool,
) error {
	go lsw.runForever(ctx, logger, logLineAnalyzer)

	return nil
}

func (lsw *LogStreamWatcher) runForever(
	ctx context.Context,
	logger *zap.Logger,
	logLineAnalyzer func(string) bool,
) {
	for {
		// Do not try to subscribe to the logs immediately. Just wait a sec
		time.Sleep(10 * time.Second)

		cmd := exec.CommandContext(ctx, lsw.cmd[0], lsw.cmd[1:]...)

		stderr, err := cmd.StderrPipe()
		if err != nil {
			logger.Error("failed to create std error pipe for the service log stream: %w", zap.Error(err))
			lsw.failureNotifier <- LogWatcherSubscriptionFailure
			continue
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			logger.Error("failed to create std out pipe for the service log stream", zap.Error(err))
			lsw.failureNotifier <- LogWatcherSubscriptionFailure
			continue
		}

		if err := cmd.Start(); err != nil {
			logger.Error("failed to start log stream process command", zap.Error(err))
			lsw.failureNotifier <- LogWatcherSubscriptionFailure
			continue
		}

		go func(reader io.ReadCloser) {
			logger.Sugar().Infof("Starting stderr logs watcher for pyth")

			scanner := bufio.NewScanner(reader)
			scanner.Split(bufio.ScanLines)
			for scanner.Scan() {
				m := scanner.Text()
				if logLineAnalyzer(m) {
					logger.Sugar().Infof("Found error in line: \"%s\"", m)
					lsw.failureNotifier <- StderrFailure
				}
			}
			if err := reader.Close(); err != nil {
				logger.Error("Cannot close stderr", zap.Error(err))
			}
		}(stderr)

		go func(reader io.ReadCloser) {
			logger.Sugar().Infof("Starting stdout logs watcher for pyth")

			scanner := bufio.NewScanner(reader)
			scanner.Split(bufio.ScanLines)
			for scanner.Scan() {
				m := scanner.Text()
				if logLineAnalyzer(m) {
					logger.Sugar().Infof("Found error in line: \"%s\"", m)
					lsw.failureNotifier <- StdoutFailure
				}
			}
			if err := reader.Close(); err != nil {
				logger.Error("Cannot close stdout", zap.Error(err))
			}
		}(stdout)

		if err := cmd.Wait(); err != nil {
			logger.Warn("Error on waiting for the log watcher", zap.Error(err))
			lsw.failureNotifier <- LogWatcherSubscriptionFailure
		}
	}
}

func failureLineAnalyzer(logLine string) bool {
	normalizedLogLine := strings.ToLower(logLine)

	return strings.Contains(normalizedLogLine, "err") ||
		strings.Contains(normalizedLogLine, "failed") ||
		strings.Contains(normalizedLogLine, "throw err") ||
		strings.Contains(normalizedLogLine, "error")
}

type ProcessManager string

const (
	EngineDocker ProcessManager = "docker"
)

type ProcessWatcher struct {
	failureNotifier chan<- FailureOnType
}

func NewProcessWatcher(notifier chan<- FailureOnType) (*ProcessWatcher, error) {
	return &ProcessWatcher{
		failureNotifier: notifier,
	}, nil
}

func (pw *ProcessWatcher) Start(ctx context.Context, logger *zap.Logger, config *config.ProcessWatcher) error {
	if config.Docker.Enabled {
		// Only docker supported for now...
		dockerCli, err := docker.New()
		if err != nil {
			return fmt.Errorf("failed to create docker client: %w", err)
		}

		go pw.monitorDockerContainer(dockerCli, ctx, logger, config.Docker.ContainerName)
	} else {
		// Only docker is supported for now
		return fmt.Errorf("only docker process manager is supported")
	}

	return nil
}

func (pw *ProcessWatcher) monitorDockerContainer(
	dockerCli *docker.Client,
	ctx context.Context,
	logger *zap.Logger,
	containerName string,
) error {
	const checkPeriod = 15 * time.Second

	logger.Sugar().Infof("Starting process watcher for docker container %s", containerName)
	failureBurst := 5
	ticker := time.NewTicker(checkPeriod)
	for {
		ticker.Reset(checkPeriod)
		select {
		case <-ctx.Done():
			logger.Info("Stopping process watcher")
			return nil
		case <-ticker.C:
			logger.Debug("Process watcher tick")
		}

		// We had enough failures, so let's report failure to the notifier
		if failureBurst < 1 {
			pw.failureNotifier <- ProcessWatcherInternalError
			failureBurst = 5
			continue
		}

		containerState, err := dockerCli.ContainerStateByName(ctx, containerName)
		if err != nil {
			logger.Error("failed to get docker container state", zap.Error(err))
			failureBurst = failureBurst - 1
			continue
		}

		// Something may be not right if it is restarting too long
		if containerState.Restarting {
			logger.Sugar().Debug(
				"Container %s is restarting. Decrementing counter(%d) before send a notification",
				containerName,
				failureBurst,
			)
			continue
		}

		if containerState.OOMKilled || containerState.ExitCode != 0 || containerState.Dead {
			logger.Sugar().Infof("Container %s has failed", containerName)
			pw.failureNotifier <- ProcessWatcherFailure
			continue
		}

		// For some reasons process is not running
		if !containerState.Running {
			logger.Sugar().Infof("Container %s is not running", containerName)
			pw.failureNotifier <- ProcessWatcherFailure
			continue
		}
	}
}

type PythServiceManager struct {
	failureNotifier <-chan FailureOnType
	stopArgs        []string
	startArgs       []string
}

func NewPythServiceManager(
	failureNotifier <-chan FailureOnType,
	stopArgs []string,
	startArgs []string,
) (*PythServiceManager, error) {
	if len(stopArgs) < 1 {
		return nil, fmt.Errorf("the stop command cannot be empty")
	}

	if len(startArgs) < 1 {
		return nil, fmt.Errorf("the start command cannot be empty")
	}

	return &PythServiceManager{
		failureNotifier: failureNotifier,
		stopArgs:        stopArgs,
		startArgs:       startArgs,
	}, nil
}

func (psm *PythServiceManager) Start(ctx context.Context, logger *zap.Logger) error {
	// Make sure we will be able to restart service immediately when even from watcher is present
	lastRestart := time.Now().Add(time.Duration(-2*RestartEverySeconds) * time.Minute)

	for {
		select {
		case <-ctx.Done():
			logger.Info("Stopping pyth service manager")
			return nil
		case reason := <-psm.failureNotifier:
			switch reason {
			case StdoutFailure:
				logger.Sugar().Warnf("Received failure from the stdout watcher")
			case StderrFailure:
				logger.Sugar().Warnf("Received failure from the stderr watcher")
			case ProcessWatcherFailure:
				logger.Sugar().Warnf("Received failure from the process watcher")
			case ProcessWatcherInternalError:
				logger.Sugar().Warnf("Received internal error from the process watcher")
			}

			nextAllowedDuration := lastRestart.Add(RestartEverySeconds * time.Second)
			if time.Now().Before(nextAllowedDuration) {
				// ignore this event. We will wait for next one as restarting may be in progress
				continue
			}

			logger.Info("Stopping pyth service")
			lastRestart = time.Now()
			err := tools.RetryRun(3, 5*time.Second, func() error {
				_, err := tools.ExecuteBinary(psm.stopArgs[0], psm.stopArgs[1:], nil)

				return err
			})

			// We do not care about errors here, just warning
			if err != nil {
				logger.Warn("failed to stop pyth service", zap.Error(err))
			}

			logger.Info("Starting pyth service")
			err = tools.RetryRun(3, 5*time.Second, func() error {
				_, err := tools.ExecuteBinary(psm.startArgs[0], psm.startArgs[1:], nil)

				return err
			})

			if err != nil {
				logger.Error("failed to start pyth service", zap.Error(err))
			}
		}
	}
}

func execute(config *config.Config) error {
	programContext, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger, err := zap.NewProduction()
	if err != nil {
		return fmt.Errorf("failed to create logger: %w", err)
	}
	defer logger.Sync()

	failureNotifier := make(chan FailureOnType)

	logStreamWatcher, err := NewLogStreamWatcher(config.Commands.LogStream, failureNotifier)
	if err != nil {
		return fmt.Errorf("failed to create new log stream process: %w", err)
	}
	if err := logStreamWatcher.Start(programContext, logger, failureLineAnalyzer); err != nil {
		return fmt.Errorf("failed to start log stream process")
	}

	processWatcher, err := NewProcessWatcher(failureNotifier)
	if err != nil {
		return fmt.Errorf("failed to create process watcher: %w", err)
	}

	if err := processWatcher.Start(programContext, logger.Named("process-watcher"), &config.ProcessWatcher); err != nil {
		return fmt.Errorf("failed to start process watcher: %w", err)
	}

	pythServiceManager, err := NewPythServiceManager(
		failureNotifier,
		config.Commands.Stop,
		config.Commands.Start,
	)
	if err != nil {
		return fmt.Errorf("failed to create pyth service manager")
	}
	pythServiceManager.Start(programContext, logger.Named("pyth-service-manager"))

	<-programContext.Done()
	return nil
}

func main() {
	rootCmd.Execute()
}
