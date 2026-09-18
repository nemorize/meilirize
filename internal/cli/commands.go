package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	bloblocal "meilirize/internal/blob/local"
	"meilirize/internal/buildinfo"
	"meilirize/internal/config"
	"meilirize/internal/mailbox"
	"meilirize/internal/storage/sqlite"
)

type configResolverFactory func() (config.Resolver, error)

func newDoctorCommand(configPath *string, newResolver configResolverFactory) *cobra.Command {
	return &cobra.Command{
		Use:     "doctor",
		Short:   "Check whether the service is ready to run",
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
			verification, err := mailbox.NewService(store, blobStore).VerifyBlobs(command.Context())
			if err != nil {
				return fmt.Errorf(
					"check blob integrity (%d checked, %d failed): %w",
					verification.Checked,
					verification.Failed,
					err,
				)
			}
			_, err = fmt.Fprintf(
				command.OutOrStdout(),
				"Configuration: OK\nDatabase: OK\nBlobs: OK (%d checked)\n",
				verification.Checked,
			)
			return err
		},
	}
}

func resolveConfigSources(configPath string, newResolver configResolverFactory) (config.Sources, error) {
	if configPath != "" {
		return (config.Resolver{}).Resolve(configPath)
	}

	resolver, err := newResolver()
	if err != nil {
		return config.Sources{}, err
	}
	return resolver.Resolve(configPath)
}

func newVersionCommand(info buildinfo.Info) *cobra.Command {
	return &cobra.Command{
		Use:     "version",
		Short:   "Show version information",
		GroupID: groupUtility,
		Args:    cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(
				command.OutOrStdout(),
				"meilirize %s\ncommit: %s\nbuilt: %s\n",
				info.Version,
				info.Commit,
				info.Date,
			)
			return err
		},
	}
}
