package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	EnvironmentPrefix             = "MEILIRIZE_"
	ConfigPathEnvironmentVariable = "MEILIRIZE_CONFIG"
	SystemConfigPath              = "/etc/meilirize/config.toml"
)

type FileSelection string

const (
	FileNotSelected           FileSelection = "none"
	FileSelectedByFlag        FileSelection = "--config"
	FileSelectedByEnvironment FileSelection = ConfigPathEnvironmentVariable
	FileSelectedAutomatically FileSelection = "automatic"
)

type Sources struct {
	FilePath          string
	FileSelection     FileSelection
	EnvironmentPrefix string
}

func (sources Sources) HasFile() bool {
	return sources.FilePath != ""
}

type Resolver struct {
	WorkingDirectory    string
	UserConfigDirectory string
	SystemPath          string
	LookupEnvironment   func(string) (string, bool)
}

func NewResolver() (Resolver, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return Resolver{}, fmt.Errorf("get working directory: %w", err)
	}

	userConfigDirectory, err := os.UserConfigDir()
	if err != nil {
		return Resolver{}, fmt.Errorf("get user config directory: %w", err)
	}

	return Resolver{
		WorkingDirectory:    workingDirectory,
		UserConfigDirectory: userConfigDirectory,
		SystemPath:          SystemConfigPath,
		LookupEnvironment:   os.LookupEnv,
	}, nil
}

func (resolver Resolver) Resolve(configPath string) (Sources, error) {
	if configPath != "" {
		return selectFile(configPath, FileSelectedByFlag)
	}

	if resolver.LookupEnvironment != nil {
		if path, ok := resolver.LookupEnvironment(ConfigPathEnvironmentVariable); ok && path != "" {
			return selectFile(path, FileSelectedByEnvironment)
		}
	}

	for _, path := range resolver.SearchPaths() {
		info, err := os.Stat(path)
		switch {
		case err == nil && info.Mode().IsRegular():
			return newSources(path, FileSelectedAutomatically), nil
		case err == nil:
			return Sources{}, fmt.Errorf("config path %q is not a regular file", path)
		case errors.Is(err, fs.ErrNotExist):
			continue
		default:
			return Sources{}, fmt.Errorf("inspect config file %q: %w", path, err)
		}
	}

	return newSources("", FileNotSelected), nil
}

func (resolver Resolver) SearchPaths() []string {
	paths := []string{
		filepath.Join(resolver.WorkingDirectory, "meilirize.toml"),
		filepath.Join(resolver.UserConfigDirectory, "meilirize", "config.toml"),
		resolver.SystemPath,
	}

	unique := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		path = filepath.Clean(path)
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		unique = append(unique, path)
	}
	return unique
}

func selectFile(path string, selection FileSelection) (Sources, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return Sources{}, fmt.Errorf("resolve config file %q: %w", path, err)
	}
	if err := requireRegularFile(absolutePath); err != nil {
		return Sources{}, err
	}
	return newSources(absolutePath, selection), nil
}

func newSources(path string, selection FileSelection) Sources {
	return Sources{
		FilePath:          path,
		FileSelection:     selection,
		EnvironmentPrefix: EnvironmentPrefix,
	}
}

func requireRegularFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("open config file %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("config path %q is not a regular file", path)
	}
	return nil
}
