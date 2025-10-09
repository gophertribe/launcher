package log

import (
	"fmt"

	"github.com/charmbracelet/log"
)

type CharmLogger struct {
	*log.Logger
}

func NewCharmLogger(logger *log.Logger) *CharmLogger {
	return &CharmLogger{Logger: logger}
}

func (l *CharmLogger) SetLevel(level string) error {
	lvl, err := log.ParseLevel(level)
	if err != nil {
		return fmt.Errorf("could not parse log level: %w", err)
	}
	l.Logger.SetLevel(lvl)
	return nil
}
