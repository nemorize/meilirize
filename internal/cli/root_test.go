package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meilirize/internal/buildinfo"
	"meilirize/internal/config"
)

var testBuild = buildinfo.Info{
	Version: "1.2.3",
	Commit:  "abc1234",
	Date:    "2026-09-18T00:00:00Z",
}

func TestRootHelpListsCommandGroups(t *testing.T) {
	output, err := executeForTest("--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"MEILIRIZE",
		"meilirize <command> [flags]",
		"Runtime",
		"serve",
		"doctor",
		"Utilities",
		"version",
		"completion",
		"--config string",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("help does not contain %q:\n%s", expected, output)
		}
	}
	for _, unwanted := range []string{
		"init", "start", "stop", "restart", "status", "daemon", "migrate",
		"config", "user", "address", "provider",
	} {
		if strings.Contains(output, "  "+unwanted+" ") {
			t.Errorf("help unexpectedly contains %q:\n%s", unwanted, output)
		}
	}
}

func TestServeHelp(t *testing.T) {
	output, err := executeForTest("serve", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "meilirize serve") || !strings.Contains(output, "Usage") {
		t.Fatalf("unexpected output:\n%s", output)
	}
}

func TestDoctorValidatesConfigurationWithoutShowingValues(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(
		configPath,
		[]byte("[smtp]\nlisten_plain = \"127.0.0.1:2525\"\nhostname = \"mail.example\"\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	output, err := executeForTest("doctor", "--config", configPath)
	if err != nil {
		t.Fatal(err)
	}
	if output != "Configuration: OK\n" {
		t.Fatalf("unexpected output: %q", output)
	}
	for _, secret := range []string{configPath, "smtp", "2525", "mail.example"} {
		if strings.Contains(output, secret) {
			t.Errorf("doctor output exposes %q: %s", secret, output)
		}
	}
}

func TestDoctorRejectsMalformedConfiguration(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("broken = [\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := executeForTest("doctor", "--config", configPath)
	if err == nil || !strings.Contains(err.Error(), "parse config file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExplicitConfigFileDoesNotInitializeAutomaticDiscovery(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	resolverCalled := false
	factory := func() (config.Resolver, error) {
		resolverCalled = true
		return config.Resolver{}, nil
	}

	sources, err := resolveConfigSources(configPath, factory)
	if err != nil {
		t.Fatal(err)
	}
	if resolverCalled {
		t.Fatal("automatic discovery was initialized for an explicit selector")
	}
	if sources.FilePath != configPath || sources.FileSelection != config.FileSelectedByFlag {
		t.Fatalf("unexpected sources: %+v", sources)
	}
}

func TestVersionCommand(t *testing.T) {
	output, err := executeForTest("version")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"meilirize 1.2.3", "commit: abc1234", "built: 2026-09-18T00:00:00Z"} {
		if !strings.Contains(output, expected) {
			t.Errorf("version output does not contain %q:\n%s", expected, output)
		}
	}
}

func TestErrorOutputIsPlainForNonTerminal(t *testing.T) {
	var output bytes.Buffer
	PrintError(&output, context.Canceled)
	if got := output.String(); got != "Error: context canceled\n" {
		t.Fatalf("error output = %q", got)
	}
}

func executeForTest(args ...string) (string, error) {
	var output bytes.Buffer
	err := Execute(context.Background(), args, strings.NewReader(""), &output, &output, testBuild)
	return output.String(), err
}
