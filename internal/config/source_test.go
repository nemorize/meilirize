package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveExplicitFileOverridesConfigPathEnvironment(t *testing.T) {
	directory := t.TempDir()
	explicitPath := filepath.Join(directory, "explicit.toml")
	environmentPath := filepath.Join(directory, "environment.toml")
	writeFile(t, explicitPath, "source = \"flag\"\n")
	writeFile(t, environmentPath, "source = \"environment\"\n")

	resolver := Resolver{
		LookupEnvironment: func(string) (string, bool) {
			return environmentPath, true
		},
	}
	sources, err := resolver.Resolve(explicitPath)
	if err != nil {
		t.Fatal(err)
	}
	if sources.FilePath != explicitPath || sources.FileSelection != FileSelectedByFlag {
		t.Fatalf("unexpected sources: %+v", sources)
	}
	if sources.EnvironmentPrefix != EnvironmentPrefix {
		t.Fatalf("environment prefix = %q", sources.EnvironmentPrefix)
	}
}

func TestResolveFileFromConfigPathEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "environment.toml")
	writeFile(t, path, "source = \"environment\"\n")
	resolver := Resolver{
		LookupEnvironment: func(name string) (string, bool) {
			if name != ConfigPathEnvironmentVariable {
				t.Fatalf("environment lookup = %q", name)
			}
			return path, true
		},
	}

	sources, err := resolver.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if sources.FilePath != path || sources.FileSelection != FileSelectedByEnvironment {
		t.Fatalf("unexpected sources: %+v", sources)
	}
}

func TestResolveExplicitFileMustExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.toml")

	_, err := (Resolver{}).Resolve(path)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveAutomaticallyUsesFirstExistingFile(t *testing.T) {
	directory := t.TempDir()
	userConfigDirectory := t.TempDir()
	systemPath := filepath.Join(t.TempDir(), "config.toml")
	workingPath := filepath.Join(directory, "meilirize.toml")
	userPath := filepath.Join(userConfigDirectory, "meilirize", "config.toml")
	writeFile(t, userPath, "source = \"user\"\n")
	writeFile(t, systemPath, "source = \"system\"\n")

	resolver := Resolver{
		WorkingDirectory:    directory,
		UserConfigDirectory: userConfigDirectory,
		SystemPath:          systemPath,
	}

	sources, err := resolver.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if sources.FilePath != userPath || sources.FileSelection != FileSelectedAutomatically {
		t.Fatalf("unexpected sources: %+v", sources)
	}

	writeFile(t, workingPath, "source = \"working\"\n")
	sources, err = resolver.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if sources.FilePath != workingPath {
		t.Fatalf("path = %q, want %q", sources.FilePath, workingPath)
	}
}

func TestResolveWithoutFileUsesEnvironmentAndDefaults(t *testing.T) {
	resolver := Resolver{
		WorkingDirectory:    t.TempDir(),
		UserConfigDirectory: t.TempDir(),
		SystemPath:          filepath.Join(t.TempDir(), "config.toml"),
	}

	sources, err := resolver.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if sources.HasFile() || sources.FileSelection != FileNotSelected {
		t.Fatalf("unexpected sources: %+v", sources)
	}
	if sources.EnvironmentPrefix != EnvironmentPrefix {
		t.Fatalf("environment prefix = %q", sources.EnvironmentPrefix)
	}
}

func TestValidateFileSyntax(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, "server = [\n")

	err := Validate(newSources(path, FileSelectedByFlag))
	if err == nil || !strings.Contains(err.Error(), "parse config file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateWithoutFile(t *testing.T) {
	t.Setenv(SMTPListenEnvironment, Defaults().SMTP.Listen)
	t.Setenv(SMTPHostnameEnvironment, Defaults().SMTP.Hostname)
	if err := Validate(newSources("", FileNotSelected)); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
