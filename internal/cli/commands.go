package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"meilirize/internal/buildinfo"
	"meilirize/internal/config"
)

type configResolverFactory func() (config.Resolver, error)

func newDoctorCommand(configPath *string, newResolver configResolverFactory) *cobra.Command {
	return &cobra.Command{
		Use:     "doctor",
		Short:   "Check whether the service is ready to run",
		GroupID: groupRuntime,
		Args:    cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			sources, err := resolveConfigSources(*configPath, newResolver)
			if err != nil {
				return err
			}
			if err := config.Validate(sources); err != nil {
				return err
			}
			_, err = fmt.Fprintln(command.OutOrStdout(), "Configuration: OK")
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

func newPlaceholderCommand(use, short, group string) *cobra.Command {
	return &cobra.Command{
		Use:     use,
		Short:   short,
		GroupID: group,
		Args:    cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return command.Help()
		},
	}
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
