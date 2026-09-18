package config

import (
	"fmt"
	"os"

	"github.com/pelletier/go-toml/v2"
)

func Validate(sources Sources) error {
	if !sources.HasFile() {
		return nil
	}

	contents, err := os.ReadFile(sources.FilePath)
	if err != nil {
		return fmt.Errorf("read config file %q: %w", sources.FilePath, err)
	}

	var document map[string]any
	if err := toml.Unmarshal(contents, &document); err != nil {
		return fmt.Errorf("parse config file %q: %w", sources.FilePath, err)
	}
	return nil
}
