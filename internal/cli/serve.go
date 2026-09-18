package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	bloblocal "meilirize/internal/blob/local"
	"meilirize/internal/config"
	"meilirize/internal/mailbox"
	"meilirize/internal/outbound"
	"meilirize/internal/provider"
	"meilirize/internal/smtpd"
	"meilirize/internal/storage/sqlite"
)

func newServeCommand(configPath *string, newResolver configResolverFactory) *cobra.Command {
	return &cobra.Command{
		Use:     "serve",
		Short:   "Run the server in the foreground",
		GroupID: groupRuntime,
		Args:    cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) (runError error) {
			sources, err := resolveConfigSources(*configPath, newResolver)
			if err != nil {
				return err
			}
			configuration, err := config.Load(sources)
			if err != nil {
				return err
			}
			gcInterval, gcGracePeriod, err := configuration.Storage.GarbageCollectionDurations()
			if err != nil {
				return err
			}
			deliveryConfiguration, err := configuration.Delivery.Runtime()
			if err != nil {
				return err
			}
			store, err := sqlite.Open(command.Context(), configuration.Database.Path)
			if err != nil {
				return err
			}
			defer func() {
				runError = errors.Join(runError, store.Close())
			}()
			blobStore, err := bloblocal.New(configuration.Storage.BlobPath)
			if err != nil {
				return err
			}
			mailboxService := mailbox.NewService(store, blobStore)
			providerRegistry := provider.NewRegistry()
			deliveryWorker, err := outbound.New(outbound.Config{
				PollInterval:  deliveryConfiguration.PollInterval,
				LeaseDuration: deliveryConfiguration.LeaseDuration,
				SendTimeout:   deliveryConfiguration.SendTimeout,
				BatchSize:     deliveryConfiguration.BatchSize,
				MaxAttempts:   deliveryConfiguration.MaxAttempts,
				RetryInitial:  deliveryConfiguration.RetryInitial,
				RetryMax:      deliveryConfiguration.RetryMax,
			}, store, mailboxService, providerRegistry)
			if err != nil {
				return err
			}
			if _, err := mailboxService.CollectGarbage(
				command.Context(),
				gcGracePeriod,
			); err != nil {
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
			group, groupContext := errgroup.WithContext(command.Context())
			group.Go(func() error {
				return server.Serve(groupContext, listeners)
			})
			group.Go(func() error {
				return mailboxService.RunGarbageCollection(
					groupContext,
					gcInterval,
					gcGracePeriod,
				)
			})
			group.Go(func() error {
				return deliveryWorker.Run(groupContext)
			})
			return group.Wait()
		},
	}
}
