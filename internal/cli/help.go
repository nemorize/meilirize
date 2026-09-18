package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/spf13/cobra"
)

type helpTheme struct {
	writer      io.Writer
	brand       lipgloss.Style
	description lipgloss.Style
	heading     lipgloss.Style
	command     lipgloss.Style
	muted       lipgloss.Style
}

func newHelpTheme(output io.Writer) helpTheme {
	return helpTheme{
		writer:      colorprofile.NewWriter(output, os.Environ()),
		brand:       lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7C6CFF")),
		description: lipgloss.NewStyle().Foreground(lipgloss.Color("#D4D4DC")),
		heading:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#AFA7FF")),
		command:     lipgloss.NewStyle().Foreground(lipgloss.Color("#74D7EC")),
		muted:       lipgloss.NewStyle().Foreground(lipgloss.Color("#8B8B98")),
	}
}

func renderHelp(command *cobra.Command, _ []string) {
	theme := newHelpTheme(command.OutOrStdout())
	renderHeader(theme, command)
	renderUsageBlock(theme, command)
	renderCommands(theme, command)
	renderFlags(theme, command)

	if command.HasAvailableSubCommands() {
		fmt.Fprintf(
			theme.writer,
			"\n%s\n  %s\n",
			theme.muted.Render("Learn more"),
			theme.muted.Render(fmt.Sprintf("%s <command> --help", command.CommandPath())),
		)
	}
}

func renderUsage(command *cobra.Command) error {
	theme := newHelpTheme(command.ErrOrStderr())
	renderUsageBlock(theme, command)
	return nil
}

func renderHeader(theme helpTheme, command *cobra.Command) {
	if command == command.Root() {
		fmt.Fprintf(theme.writer, "%s\n%s\n", theme.brand.Render("MEILIRIZE"), theme.description.Render(command.Short))
		return
	}
	fmt.Fprintf(theme.writer, "%s\n%s\n", theme.brand.Render(command.CommandPath()), theme.description.Render(command.Short))
}

func renderUsageBlock(theme helpTheme, command *cobra.Command) {
	usage := command.UseLine()
	if command.HasAvailableSubCommands() {
		usage = command.CommandPath() + " <command>"
		if command.HasAvailableFlags() {
			usage += " [flags]"
		}
	}
	fmt.Fprintf(theme.writer, "\n%s\n  %s\n", theme.heading.Render("Usage"), usage)
}

func renderCommands(theme helpTheme, command *cobra.Command) {
	available := availableCommands(command)
	if len(available) == 0 {
		return
	}

	if len(command.Groups()) == 0 {
		renderCommandGroup(theme, "Commands", available)
		return
	}
	for _, group := range command.Groups() {
		var grouped []*cobra.Command
		for _, child := range available {
			if child.GroupID == group.ID {
				grouped = append(grouped, child)
			}
		}
		if len(grouped) > 0 {
			renderCommandGroup(theme, group.Title, grouped)
		}
	}
}

func renderCommandGroup(theme helpTheme, title string, commands []*cobra.Command) {
	width := 0
	for _, command := range commands {
		if len(command.Name()) > width {
			width = len(command.Name())
		}
	}
	fmt.Fprintf(theme.writer, "\n%s\n", theme.heading.Render(title))
	for _, command := range commands {
		name := command.Name() + strings.Repeat(" ", width-len(command.Name()))
		fmt.Fprintf(theme.writer, "  %s  %s\n", theme.command.Render(name), command.Short)
	}
}

func renderFlags(theme helpTheme, command *cobra.Command) {
	flags := command.NonInheritedFlags()
	if flags.HasAvailableFlags() {
		fmt.Fprintf(theme.writer, "\n%s\n%s", theme.heading.Render("Flags"), flags.FlagUsages())
	}
	inherited := command.InheritedFlags()
	if inherited.HasAvailableFlags() {
		fmt.Fprintf(theme.writer, "\n%s\n%s", theme.heading.Render("Global flags"), inherited.FlagUsages())
	}
}

func availableCommands(command *cobra.Command) []*cobra.Command {
	var available []*cobra.Command
	for _, child := range command.Commands() {
		if child.IsAvailableCommand() || child.Name() == "help" {
			available = append(available, child)
		}
	}
	sort.SliceStable(available, func(i, j int) bool {
		return available[i].Name() < available[j].Name()
	})
	return available
}

func PrintError(output io.Writer, err error) {
	if err == nil {
		return
	}
	writer := colorprofile.NewWriter(output, os.Environ())
	label := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF6B6B")).Render("Error:")
	fmt.Fprintf(writer, "%s %s\n", label, err)
}
