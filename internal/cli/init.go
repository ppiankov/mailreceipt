package cli

import (
	"fmt"

	"github.com/ppiankov/mailreceipt/internal/config"
	"github.com/spf13/cobra"
)

// initCmd scaffolds a .mailreceipt.yml in the working directory so an operator
// who runs mailreceipt repeatedly on one server can set --log/--log-year/--case
// defaults once. check and verify read it when present; flags still override.
func initCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a .mailreceipt.yml with project defaults",
		Long: "Creates a .mailreceipt.yml in the current directory holding default\n" +
			"values for --log, --log-year, and a case prefix. check and verify use\n" +
			"these as defaults; an explicit flag always overrides the config.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.Write(config.FileName, force); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", config.FileName)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing "+config.FileName)
	return cmd
}
