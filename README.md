# Process Visor

The Sidecar Service is a binary designed to subscribe to a specified log stream and take action based on configured parameters. It's particularly useful for monitoring and managing the health of a service by automatically restarting it in case of failure or upon detecting specific keywords in the log stream.

## Getting Started

To get started with the Sidecar Service, follow these steps:

1. Clone the Repository: Clone the repository to your local machine.

```
git clone https://github.com/vegaprotocol/process-watcher.git
```

2. Build the binary
```
go build -o ./process-watcher . 
```

3. Copy example config and update it

```
cp config.example.toml config.toml
```

4. Run the program

```
./process-watcher -c ./config.toml
```

## Configuration

```toml
[commands]
    # Define the log stream command
    log-stream = [
        "journalctl",
        "-u", "pyth-price-pusher",
        "-n", "1",
        "-f",
    ]
    # Define the command used to stop the service
    stop = ["systemclt", "stop", "pyth-price-pusher"]
    # Define the command to start the service
    start = ["systemclt", "start", "pyth-price-pusher"]


[process-watcher]
    # Watches if specific docker container is running
    [process-watcher.docker]
        enabled = true
        container-name = "pyth-price-pusher"

[logs-watcher]
    # Define the keywords that trigger process restart
    failure-keywords = ["err", "failed", "throw err", "error"]
```