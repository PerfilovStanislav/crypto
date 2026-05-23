package shutdown

import (
	"context"
	"fmt"
	"io"
	"logger"
	"os"
	"time"
)

func New(log *logger.Logger) *Manager {
	return &Manager{
		log: log,
	}
}

// Manager управляет закрытием ресурсов
type Manager struct {
	closers []io.Closer
	log     *logger.Logger
}

func (c *Manager) Add(closer io.Closer) {
	c.closers = append(c.closers, closer)
}

func (c *Manager) CloseAll() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan struct{})

	go func() {
		// Закрываем в обратном порядке (LIFO)
		for i := len(c.closers) - 1; i >= 0; i-- {
			if err := c.closers[i].Close(); err != nil {
				if c.log != nil {
					c.log.Error("failed to close resource", "error", err)
				} else {
					_, _ = fmt.Fprintf(os.Stderr, "failed to close resource: %v\n", err)
				}
			}
		}
		close(done)
	}()

	select {
	case <-done:
		// Все закрылось успешно
	case <-ctx.Done():
		if c.log != nil {
			c.log.Error("graceful shutdown timeout exceeded")
		} else {
			_, _ = fmt.Fprintln(os.Stderr, "graceful shutdown timeout exceeded")
		}
	}
}
