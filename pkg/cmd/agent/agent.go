// Copyright Contributors to the Open Cluster Management project
package agent

import (
	"context"

	"github.com/spf13/cobra"
	"k8s.io/apiserver/pkg/server"
	"k8s.io/klog"

	"open-cluster-management.io/multicluster-controlplane/pkg/agent"
)

func NewAgent() *cobra.Command {
	agentOptions := agent.NewAgentOptions()

	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Start a Multicluster Controlplane Agent",
		RunE: func(cmd *cobra.Command, args []string) error {
			shutdownCtx, cancel := context.WithCancel(context.TODO())

			shutdownHandler := server.SetupSignalHandler()
			go func() {
				defer cancel()
				<-shutdownHandler
				klog.Infof("Received SIGTERM or SIGINT signal, shutting down agent.")
			}()

			ctx, terminate := context.WithCancel(shutdownCtx)
			defer terminate()

			if err := agentOptions.RunAgent(ctx); err != nil {
				return err
			}

			<-ctx.Done()
			return nil
		},
	}

	flags := cmd.Flags()
	agentOptions.AddFlags(flags)
	return cmd
}
