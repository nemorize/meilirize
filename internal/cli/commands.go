package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"meilirize/internal/buildinfo"
)

func newConfigCommand() *cobra.Command {
	command := newCommandGroup("config", "Inspect and validate configuration")
	command.AddCommand(
		newPlaceholderCommand("show", "Show the resolved configuration", ""),
		newPlaceholderCommand("validate", "Validate the configuration", ""),
	)
	return command
}

func newUserCommand() *cobra.Command {
	command := newCommandGroup("user", "Manage mailbox users")
	command.AddCommand(
		newPlaceholderCommand("add", "Add a user", ""),
		newPlaceholderCommand("list", "List users", ""),
		newPlaceholderCommand("show", "Show a user", ""),
		newPlaceholderCommand("enable", "Enable a user", ""),
		newPlaceholderCommand("disable", "Disable a user", ""),
		newPlaceholderCommand("password", "Change a user's password", ""),
	)
	return command
}

func newAddressCommand() *cobra.Command {
	command := newCommandGroup("address", "Manage mailbox addresses")
	command.AddCommand(
		newPlaceholderCommand("add", "Add an address", ""),
		newPlaceholderCommand("list", "List addresses", ""),
		newPlaceholderCommand("remove", "Remove an address", ""),
		newPlaceholderCommand("set-primary", "Set a user's primary address", ""),
	)
	return command
}

func newProviderCommand() *cobra.Command {
	command := newCommandGroup("provider", "Manage email providers")
	command.AddCommand(
		newPlaceholderCommand("add", "Add a provider", ""),
		newPlaceholderCommand("list", "List providers", ""),
		newPlaceholderCommand("show", "Show a provider", ""),
		newPlaceholderCommand("test", "Test a provider connection", ""),
		newPlaceholderCommand("enable", "Enable a provider", ""),
		newPlaceholderCommand("disable", "Disable a provider", ""),
		newPlaceholderCommand("remove", "Remove a provider", ""),
	)
	return command
}

func newCommandGroup(use, short string) *cobra.Command {
	return &cobra.Command{
		Use:     use,
		Short:   short,
		GroupID: groupManagement,
	}
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
