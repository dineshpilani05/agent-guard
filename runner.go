package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"time"
)

func startProcess(commandName string, commandArgs []string, maxRunTime time.Duration, extraEnv []string) (*exec.Cmd, io.ReadCloser, context.Context, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(context.Background(), maxRunTime)

	cmd := exec.CommandContext(ctx, commandName, commandArgs...)

	// Start with everything already in our own environment (PATH, etc.),
	// then add our extra variables (like pointing the agent at our proxy).
	cmd.Env = append(os.Environ(), extraEnv...)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, nil, nil, nil, err
	}

	return cmd, stdoutPipe, ctx, cancel, nil
}
