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

			resolvedListeners, err := configuration.SMTP.ResolvedListeners()
			if err != nil {
				return err
			}
			listenerConfigurations := make([]smtpd.ListenerConfig, 0, len(resolvedListeners))
			for _, listener := range resolvedListeners {
				listenerConfigurations = append(listenerConfigurations, smtpd.ListenerConfig{
					Address: listener.Address,
					Mode:    smtpd.Mode(listener.Mode),
				})
			}

			server, err := smtpd.New(smtpd.Config{
				Listeners:   listenerConfigurations,
				Hostname:    configuration.SMTP.Hostname,
				TLSCertFile: configuration.SMTP.TLS.CertFile,
				TLSKeyFile:  configuration.SMTP.TLS.KeyFile,
			})
			if err != nil {
				return err
			}
			listeners, err := server.Listen(command.Context())
			if err != nil {
				return err
			}
			for _, listener := range listeners {
				if _, err := fmt.Fprintf(
					command.OutOrStdout(),
					"SMTP listening on %s (%s)\n",
					listener.Addr(),
					listener.Mode(),
				); err != nil {
					for _, listener := range listeners {
						_ = listener.Close()
					}
					return err
				}
			}
			return server.Serve(command.Context(), listeners)
		},
	}
}
