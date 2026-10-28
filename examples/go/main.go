// Command example demonstrates embedding the updater library.
//
// Usage:
//
//	go run ./examples/go updater.json 1.4.2
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	wmlupdate "github.com/yxxbc/wmlUpdate"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: update-example updater.json 1.4.2")
		os.Exit(2)
	}
	configPath, current := os.Args[1], os.Args[2]

	u, err := wmlupdate.New(wmlupdate.Options{
		ConfigPath:     configPath,
		CurrentVersion: current,
		Events: func(e wmlupdate.Event) {
			switch e.Type {
			case wmlupdate.EventProgress:
				if e.Phase == "download" {
					fmt.Printf("downloading: %.0f%%\n", e.Percent)
				}
			case wmlupdate.EventWarning:
				fmt.Fprintln(os.Stderr, "warning:", e.Message)
			case wmlupdate.EventLog:
				fmt.Printf("[%s] %s\n", e.Phase, e.Message)
			}
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "configure updater:", err)
		os.Exit(1)
	}
	defer u.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	update, err := u.Check(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "check failed [%s]: %v\n", wmlupdate.Code(err), err)
		os.Exit(1)
	}
	if !update.Available {
		fmt.Printf("up to date: %s\n", update.CurrentVersion)
		return
	}

	fmt.Printf("update available: %s -> %s\n", update.CurrentVersion, update.Version)
	result, err := u.Apply(ctx, update, wmlupdate.ApplyOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "apply failed [%s]: %v\n", wmlupdate.Code(err), err)
		os.Exit(1)
	}
	encoded, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(encoded))
}
