package logger

import (
	"config"
	"log"
	"log/slog"
	"time"

	"github.com/DeRuina/timberjack"
)

type Config struct {
	Level string
	Path  string
	Days  int
	Size  int
}

type Logger struct {
	*slog.Logger
	rotator *timberjack.Logger
}

func New(cfg config.LogConfig) *Logger {
	rotator := &timberjack.Logger{
		Filename:         cfg.Path,       // Choose an appropriate path
		MaxSize:          cfg.Size,       // megabytes
		MaxAge:           cfg.Days,       // days
		RotationInterval: 24 * time.Hour, // Rotate daily if no other rotation met
	}

	logger := slog.New(slog.NewJSONHandler(rotator, getOptions(cfg)))

	return &Logger{logger, rotator}
}

func (l *Logger) Close() error {
	return l.rotator.Close()
}

func (l *Logger) StdLogger(level slog.Level) *log.Logger {
	return slog.NewLogLogger(l.Handler(), level)
}

func getOptions(cfg config.LogConfig) *slog.HandlerOptions {
	var lvl slog.Level

	if err := lvl.UnmarshalText([]byte(cfg.Level)); err != nil {
		lvl = slog.LevelInfo
	}

	return &slog.HandlerOptions{
		Level: lvl,
	}
}
