// Command dbsync synchronizes explicitly configured individual files
// between local filesystem paths and specific Dropbox file paths.
package main

import (
	"fmt"
	"os"
)

// version is set by the linker at build time via -ldflags, e.g.:
//
//	go build -ldflags "-X main.version=1.2.3"
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "auth":
		err = runAuthCommand(args)
	case "config":
		err = runConfigCommand(args)
	case "sync":
		err = runSyncCommand(args)
	case "status":
		err = runStatusCommand(args)
	case "run":
		err = runRunCommand(args)
	case "version":
		fmt.Printf("dbsync version %s\n", version)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		printUsage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprint(os.Stderr, `dbsync - synchronize specific files between local disk and Dropbox

Usage:
  dbsync auth login              Begin first-time Dropbox authorization and obtain a refresh token
  dbsync auth status             Show whether Dropbox credentials are configured and valid
  dbsync config validate         Validate the YAML configuration without contacting Dropbox
  dbsync sync --once             Run a single reconciliation cycle and exit
  dbsync sync --dry-run          Run a single reconciliation cycle without making any changes
  dbsync status                  Show persisted synchronization state for every mapping
  dbsync run                     Run the continuous synchronization service (default Docker command)
  dbsync version                 Print the dbsync version

Environment variables:
  DBSYNC_CONFIG           Path to the YAML configuration file (default: /app/config.yaml)
  DROPBOX_APP_KEY         Dropbox app key (required)
  DROPBOX_APP_SECRET      Dropbox app secret (required)
  DROPBOX_REFRESH_TOKEN   Dropbox refresh token obtained via 'dbsync auth login' (required for sync/run)
`)
}
