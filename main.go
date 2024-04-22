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
	"go.uber.org/zap"
)

type FailureOnType string

const (
	StdoutFailure               FailureOnType = "stdout"
	StderrFailure               FailureOnType = "stderr"
	ProcessWatcherFailure       FailureOnType = "process-watcher"
	ProcessWatcherInternalError FailureOnType = "process-watcher-internal-error"
)

const (
	RestartEverySeconds = 60 // We won't restart more often than time defined here
)

var (
	// Used for flags.
	logsStreamCommand []string
	stopCommand       []string
	startCommand      []string
	pythContainerName string

	rootCmd = &cobra.Command{
		Use:   "pyth-service-monitor",
		Short: "A command used to analyze and restart pyth-price-pusher",
		Run: func(cmd *cobra.Command, args []string) {
			if err := execute(); err != nil {
				panic(err)
			}
		},
	}
)

func init() {
	rootCmd.PersistentFlags().StringSliceVarP(
		&logsStreamCommand,
		"logs-stream-command",
		"l",
		[]string{"journalctl", "-u", "pyth-price-pusher-primary", "-f"},
		"Command used to stream logs for the pyth price pusher",
	)
	rootCmd.PersistentFlags().StringSliceVarP(
		&stopCommand,
		"stop-command",
		"s",
		[]string{"systemctl", "stop", "pyth-price-pusher-primary"},
		"Command used to stop service if any failure detected",
	)
	rootCmd.PersistentFlags().StringSliceVarP(
		&startCommand,
		"start-command",
		"r",
		[]string{"systemctl", "start", "pyth-price-pusher-primary"},
		"Command used to start service if any failure detected",
	)
	rootCmd.PersistentFlags().StringVarP(
		&pythContainerName,
		"docker-container-name",
		"n",
		"pyth-price-pusher-primary",
		"Name of the docker container which runs pyth",
	)

}

type LogStreamWatcher struct {
	cmd *exec.Cmd

	stdErr          io.ReadCloser
	stdOut          io.ReadCloser
	failureNotifier chan<- FailureOnType
}

func NewLogStreamWatcher(logsStreamCommand []string, notifier chan<- FailureOnType) (*LogStreamWatcher, error) {
	if len(logsStreamCommand) < 1 {
		return nil, fmt.Errorf("invalid command for -logs-stream-command")
	}
	cmd := exec.Command(logsStreamCommand[0], logsStreamCommand[1:]...)

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create std error pipe for the service log stream: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create std out pipe for the service log stream: %w", err)
	}

	return &LogStreamWatcher{
		cmd:             cmd,
		stdErr:          stderr,
		stdOut:          stdout,
		failureNotifier: notifier,
	}, nil
}

func (lsp *LogStreamWatcher) Wait() error {
	return lsp.cmd.Wait()
}

func (lsp *LogStreamWatcher) Start(logger *zap.Logger, ctx context.Context, logLineAnalyzer func(string) bool) error {
	if err := lsp.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start log stream process command: %w", err)
	}

	go func(reader io.ReadCloser) {
		logger.Sugar().Infof("Starting stderr logs watcher for pyth")

		scanner := bufio.NewScanner(reader)
		scanner.Split(bufio.ScanLines)
		for scanner.Scan() {
			m := scanner.Text()
			if logLineAnalyzer(m) {
				logger.Sugar().Infof("Found error in line: \"%s\"", m)
				lsp.failureNotifier <- StderrFailure
			}
		}
		if err := reader.Close(); err != nil {
			logger.Error("Cannot close stderr", zap.Error(err))
		}
	}(lsp.stdErr)

	go func(reader io.ReadCloser) {
		logger.Sugar().Infof("Starting stdout logs watcher for pyth")

		scanner := bufio.NewScanner(reader)
		scanner.Split(bufio.ScanLines)
		for scanner.Scan() {
			m := scanner.Text()
			if logLineAnalyzer(m) {
				logger.Sugar().Infof("Found error in line: \"%s\"", m)
				lsp.failureNotifier <- StdoutFailure
			}
		}
		if err := reader.Close(); err != nil {
			logger.Error("Cannot close stdout", zap.Error(err))
		}
	}(lsp.stdOut)

	return nil
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

func (pw *ProcessWatcher) StartForDocker(ctx context.Context, logger *zap.Logger, containerName string) error {
	// Only docker supported for now...
	dockerCli, err := docker.New()
	if err != nil {
		return fmt.Errorf("failed to create docker client: %w", err)
	}

	go pw.monitorDockerContainer(dockerCli, ctx, logger, containerName)

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
	return &PythServiceManager{
		failureNotifier: failureNotifier,
		stopArgs:        stopArgs,
		startArgs:       startArgs,
	}, nil
}

func (psm *PythServiceManager) Start(ctx context.Context, logger *zap.Logger) error {
	// lastRestart := time.Now().Add(time.Duration(-60) * time.Minute)

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

			// nextAllowedDuration := time.Now().Add(RestartEverySeconds * time.Second)

		}
	}
}

func execute() error {
	programContext, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger, err := zap.NewProduction()
	if err != nil {
		return fmt.Errorf("failed to create logger: %w", err)
	}
	defer logger.Sync()

	failureNotifier := make(chan FailureOnType)

	logStreamWatcher, err := NewLogStreamWatcher(logsStreamCommand, failureNotifier)
	if err != nil {
		return fmt.Errorf("failed to create new log stream process: %w", err)
	}
	if err := logStreamWatcher.Start(logger, programContext, failureLineAnalyzer); err != nil {
		return fmt.Errorf("failed to start log stream process")
	}

	processWatcher, err := NewProcessWatcher(failureNotifier)
	if err != nil {
		return fmt.Errorf("failed to create process watcher: %w", err)
	}

	if pythContainerName != "" {
		if err := processWatcher.StartForDocker(
			programContext,
			logger.Named("docker-process-watcher"),
			pythContainerName,
		); err != nil {
			return fmt.Errorf("failed to start process watcher for docker: %w", err)
		}
	} else {
		// Only docker is supported for now
		return fmt.Errorf("only docker process manager is supported")
	}

	logStreamWatcher.Wait() // Wait as long as stream is available
	cancel()                // stop other processes

	return nil
}

func main() {
	rootCmd.Execute()
}
