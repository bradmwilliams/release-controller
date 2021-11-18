package main

import (
	migrationtool "github.com/openshift/release-controller/pkg/cmd/migration-tool"
	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
	"os"
)

func main() {
	command := NewMigrationToolCommand()
	code := cli.Run(command)
	os.Exit(code)
}

func NewMigrationToolCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migration-tool",
		Short: "OpenShift Release Payload Migration Tool",
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
			os.Exit(1)
		},
	}
	cmd.AddCommand(migrationtool.NewMigrationToolCommand("run"))
	return cmd
}
