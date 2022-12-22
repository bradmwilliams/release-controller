package main

import (
	releasepayloadsync "github.com/openshift/release-controller/pkg/cmd/release-payload-sync"
	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
	"os"
)

func main() {
	command := NewReleasePayloadSyncCommand()
	code := cli.Run(command)
	os.Exit(code)
}

func NewReleasePayloadSyncCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release-payload-sync",
		Short: "OpenShift Release Payload Sync tool",
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
			os.Exit(1)
		},
	}

	cmd.AddCommand(releasepayloadsync.NewReleasePayloadSyncCommand("start"))
	return cmd
}
