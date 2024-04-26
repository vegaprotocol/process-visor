package config

import (
	"fmt"
	"os"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/vegaprotocol/process-watcher/internal/tools"
)

type Commands struct {
	LogStream []string `toml:"log-stream"`
	Stop      []string `toml:"stop"`
	Start     []string `toml:"start"`
}

type DockerProcessWatcher struct {
	Enabled       bool   `toml:"enabled"`
	ContainerName string `toml:"container-name"`
}

type ProcessWatcher struct {
	Docker DockerProcessWatcher `toml:"docker"`
}

type LogsWatcher struct {
	FailureKeywords []string `toml:"failure-keywords"`
}

type Config struct {
	Commands       Commands       `toml:"commands"`
	ProcessWatcher ProcessWatcher `toml:"process-watcher"`
	LogsWatcher    LogsWatcher    `toml:"logs-watcher"`
}

func DefaultConfig() Config {
	return Config{
		Commands: Commands{
			LogStream: []string{
				"docker", "logs", "pyth-price-pusher",
				"-n", "1",
				"-f",
			},
			Stop: []string{
				"bash", "-c",
				"docker stop pyth-price-pusher && docker rm pyth-price-pusher",
			},
			Start: []string{
				"docker", "run",
				"--name", "pyth-price-pusher",
				"-v", fmt.Sprintf("%s/.pyth-config:/config", "...."),
				"public.ecr.aws/pyth-network/xc-price-pusher:v6.6.0",
				"--",
				"evm",
				"--endpoint", "https://rpc.ankr.com/gnosis/XXXXXXXXXXX",
				"--mnemonic-file", "/config/mnemonic",
				"--pyth-contract-address", "0x2880aB155794e7179c9eE2e38200202908C17B43",
				"--price-service-endpoint", "https://hermes.pyth.network",
				"--price-config-file", "/config/price-config.yaml",
				"--polling-frequency", "5",
			},
		},
		ProcessWatcher: ProcessWatcher{
			Docker: DockerProcessWatcher{
				Enabled:       true,
				ContainerName: "pyth-price-pusher",
			},
		},
		LogsWatcher: LogsWatcher{
			FailureKeywords: []string{"err", "failed", "throw err", "error"},
		},
	}
}

func ReadFromFile(filePath string) (*Config, error) {
	if !tools.FileExists(filePath) {
		return nil, fmt.Errorf("config file does not exists")
	}

	configContent, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	result := DefaultConfig()
	if err := toml.Unmarshal(configContent, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config file: %w", err)
	}

	return &result, nil
}
