package cli

import "github.com/spf13/cobra"

// Execute runs the root CLI command.
func Execute() error {
	root := NewRootCmd()
	return root.Execute()
}

// NewRootCmd constructs the root command with all subcommands registered.
func NewRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "filegate",
		Short: "Filegate CLI",
		Long:  "Filegate CLI provides local service operations and health/status checks.",
		Example: "  filegate serve --config /etc/filegate/conf.yaml\n" +
			"  filegate health\n" +
			"  filegate status\n" +
			"  filegate index rescan --new",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddCommand(newDaemonServeCmd())
	cmd.AddCommand(newDaemonIndexCmd())
	cmd.AddCommand(newHealthCmd())
	cmd.AddCommand(newStatusCmd())
	return cmd
}
