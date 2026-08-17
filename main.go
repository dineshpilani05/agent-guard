package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
)

func main() {
	timeoutFlag := flag.Duration("timeout", 60*1e9, "max time the process is allowed to run")
	windowFlag := flag.Int("window", 15, "how many recent lines to remember for repeat detection")
	thresholdFlag := flag.Int("threshold", 6, "how many times a line can repeat before tripping")
	proxyPortFlag := flag.String("port", "8080", "local port the proxy listens on")
	proxyTargetFlag := flag.String("target", "https://api.anthropic.com", "the real API the proxy forwards to")
	toolWindowFlag := flag.Int("tool-window", 10, "how many recent tool calls to remember for repeat detection")
	toolThresholdFlag := flag.Int("tool-threshold", 4, "how many times an identical tool call can repeat before tripping")
	flag.Parse()

	remainingArgs := flag.Args()
	if len(remainingArgs) < 1 {
		fmt.Println("agent-guard — a circuit breaker for AI agent sessions")
		fmt.Println("\nUsage: agent-guard [flags] <command> [args...]")
		fmt.Println("\nFlags:")
		flag.PrintDefaults()
		os.Exit(1)
	}

	commandName := remainingArgs[0]
	commandArgs := remainingArgs[1:]

	// Configure the shared tool-call watcher with the user's chosen thresholds
	// instead of the hardcoded values it used during development.
	toolCallWatcher = newRepeatWatcher(*toolWindowFlag, *toolThresholdFlag)

	var runningCmd *os.Process

	onBreakerTrip = func(reason string) {
		fmt.Println("Killing agent process due to:", reason)
		if runningCmd != nil {
			runningCmd.Kill()
		}
	}

	go func() {
		err := startProxy(*proxyPortFlag, *proxyTargetFlag)
		if err != nil {
			fmt.Println("Proxy failed:", err)
		}
	}()

	extraEnv := []string{"ANTHROPIC_BASE_URL=http://localhost:" + *proxyPortFlag}

	cmd, stdoutPipe, ctx, cancel, err := startProcess(commandName, commandArgs, *timeoutFlag, extraEnv)
	if err != nil {
		fmt.Println("Failed to set up process:", err)
		os.Exit(1)
	}
	defer cancel()

	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		fmt.Println("Failed to start command:", err)
		os.Exit(1)
	}

	runningCmd = cmd.Process

	watcher := newRepeatWatcher(*windowFlag, *thresholdFlag)
	scanner := bufio.NewScanner(stdoutPipe)
	lineNumber := 0

	for scanner.Scan() {
		line := scanner.Text()
		lineNumber++
		fmt.Printf("[line %d] %s\n", lineNumber, line)

		if watcher.observe(line) {
			fmt.Println("\n🚨 CIRCUIT BREAKER TRIPPED (repeat loop detected) 🚨")
			fmt.Printf("This line appeared %d times in the last %d lines:\n", watcher.countFor(line), *windowFlag)
			fmt.Println("  " + line)
			fmt.Println("Killing the process...")
			cmd.Process.Kill()
			os.Exit(1)
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Println("Error reading command output:", err)
		os.Exit(1)
	}

	err = cmd.Wait()

	if ctx.Err() == context.DeadlineExceeded {
		fmt.Println("\n🚨 CIRCUIT BREAKER TRIPPED (timeout exceeded) 🚨")
		fmt.Printf("Process ran longer than %v and was killed.\n", *timeoutFlag)
		os.Exit(1)
	}

	if err != nil {
		fmt.Println("Command finished with error:", err)
		os.Exit(1)
	}
}
