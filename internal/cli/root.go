package cli

import (
	"context"
	"io"

	"github.com/spf13/cobra"

	"meilirize/internal/buildinfo"
)

const (
	groupRuntime    = "runtime"
	groupManagement = "management"
	groupUtility    = "utility"
)

func Execute(
	ctx context.Context,
	args []string,
	in io.Reader,
	out io.Writer,
	errOut io.Writer,
	info buildinfo.Info,
) error {
	command := NewRootCommand(info)
	command.SetArgs(args)
	command.SetIn(in)
	command.SetOut(out)
	command.SetErr(errOut)
	return command.ExecuteContext(ctx)
}

func NewRootCommand(info buildinfo.Info) *cobra.Command {
	root := &cobra.Command{
		Use:           "meilirize",
		Short:         "A self-hosted mailbox backed by email APIs",
		Version:       info.Version,
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetVersionTemplate("meilirize {{.Version}}\n")
	root.SetHelpFunc(renderHelp)
	root.SetUsageFunc(renderUsage)
	root.AddGroup(
		&cobra.Group{ID: groupRuntime, Title: "Runtime"},
		&cobra.Group{ID: groupManagement, Title: "Management"},
		&cobra.Group{ID: groupUtility, Title: "Utilities"},
	)
	root.SetHelpCommandGroupID(groupUtility)
	root.SetCompletionCommandGroupID(groupUtility)

	root.AddCommand(
		newPlaceholderCommand("serve", "Run the server in the foreground", groupRuntime),
		newPlaceholderCommand("doctor", "Check whether the service is ready to run", groupRuntime),
		newConfigCommand(),
		newUserCommand(),
		newAddressCommand(),
		newProviderCommand(),
		newVersionCommand(info),
	)
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	return root
}
