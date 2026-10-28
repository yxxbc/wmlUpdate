// Command example runs wml-update and consumes its NDJSON event stream.
//
// Usage:
//
//	go run ./examples/go updater.json 1.4.2 [path/to/wml-update]
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type event struct {
	Type    string          `json:"type"`
	Phase   string          `json:"phase"`
	Message string          `json:"message"`
	Percent float64         `json:"percent"`
	Data    json.RawMessage `json:"data"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: update-example updater.json 1.4.2 [path/to/wml-update]")
		os.Exit(2)
	}
	config, current := os.Args[1], os.Args[2]
	binary := "wml-update"
	if len(os.Args) > 3 {
		binary = os.Args[3]
	}
	if v := os.Getenv("WML_UPDATE"); v != "" && len(os.Args) <= 3 {
		binary = v
	}

	cmd := exec.CommandContext(context.Background(), binary,
		"--config", config, "--json", "update", "--current-version", current)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "start updater:", err)
		os.Exit(1)
	}

	terminal := false
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			fmt.Fprintln(os.Stderr, "invalid JSON from updater:", line)
			_ = cmd.Process.Kill()
			break
		}
		switch ev.Type {
		case "progress":
			if ev.Phase == "download" {
				fmt.Printf("downloading: %.0f%%\n", ev.Percent)
			}
		case "warning":
			fmt.Fprintln(os.Stderr, "warning:", ev.Message)
		case "log":
			fmt.Printf("[%s] %s\n", ev.Phase, ev.Message)
		case "error":
			fmt.Fprintf(os.Stderr, "update failed [%s]: %s\n", ev.Error.Code, ev.Error.Message)
			terminal = true
		case "result":
			var pretty any
			_ = json.Unmarshal(ev.Data, &pretty)
			encoded, _ := json.MarshalIndent(pretty, "", "  ")
			fmt.Println(string(encoded))
			terminal = true
		}
	}
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		os.Exit(1)
	}
	if !terminal {
		fmt.Fprintln(os.Stderr, "updater exited without a terminal event")
		os.Exit(1)
	}
}
