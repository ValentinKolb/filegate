// Package cli implements the Filegate command-line interface using Cobra and Viper.
// It provides the serve, index, health, and status commands and handles
// configuration loading from YAML files and environment variables.
//
// Key Components:
//
//   - Execute: entry point invoked by main.
//   - NewRootCmd: constructs the root command with all subcommands.
//   - serve: starts the HTTP server with detector, index, and store wiring.
//   - index rescan: triggers a full or incremental index rebuild.
//
// Related Packages:
//
//   - domain: provides Service, Config, and port interfaces wired by CLI.
//   - adapter/http: provides NewRouter consumed by the serve command.
//   - infra/*: infrastructure adapters initialized during serve startup.
package cli
