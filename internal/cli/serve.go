package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"meilirize/internal/config"
	"meilirize/internal/smtpd"
)

func newServeCommand(configPath *string, newResolver configResolverFactory) *cobra.Command {
	return &cobra.Command{
		Use:     "serve",
		Short:   "Run the server in the foreground",
		GroupID: groupRuntime,
		Args:    cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			sources, err := resolveConfigSources(*configPath, newResolver)
			if err != nil {
				return err
			}
			configuration, err := config.Load(sources)
			if err != nil {
				return err
			}

			server, err := smtpd.New(smtpd.Config{
				ListenAddress: configuration.SMTP.Listen,
				Hostname:      configuration.SMTP.Hostname,
			})
			if err != nil {
				return err
			}
			listener, err := server.Listen(command.Context())
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(command.OutOrStdout(), "SMTP listening on %s\n", listener.Addr()); err != nil {
				_ = listener.Close()
				return err
			}
			return server.Serve(command.Context(), listener)
		},
	}
}
