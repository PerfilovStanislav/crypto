package server

import (
	"config"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"logger"
	"net/http"
	"source"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"
)

type contextKey string

const (
	loggerKey contextKey = "logger"
)

type Server struct {
	*http.Server
	quotes source.Quotes
	log    *logger.Logger
}

func New(log *logger.Logger, cfg config.HttpConfig, quotes source.Quotes) *Server {
	ser := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		ErrorLog:     log.StdLogger(slog.LevelError),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	s := &Server{ser, quotes, log}
	s.Handler = s.router()

	return s
}

func (s *Server) Run(ctx context.Context, g *errgroup.Group) {
	g.Go(func() error {
		if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server error: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		<-ctx.Done()
		return s.Close()
	})
}

func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.Shutdown(ctx)
}

type handlerFunc func(w http.ResponseWriter, r *http.Request) error
type handlerFuncWithBody func(w http.ResponseWriter, body []byte) error

func (s *Server) router() http.Handler {
	mux := http.NewServeMux()

	var apis = map[string]handlerFuncWithBody{
		"get_quotes": s.getQuotes,
	}

	for api, fun := range apis {
		mux.HandleFunc("/api/"+api, s.corsHandler(s.tracing(s.post(fun))))
	}

	return mux
}

func (s *Server) corsHandler(next func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next(w, r)
	}
}

func (s *Server) tracing(next handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log := s.log.Logger
		traceparent := r.Header.Get("traceparent")
		var traceID, spanID string

		if traceparent != "" {
			parts := strings.Split(traceparent, "-")
			if len(parts) == 4 {
				traceID = parts[1]
				spanID = parts[2]
			}
		}

		if traceID == "" {
			b := make([]byte, 16)
			binary.BigEndian.PutUint32(b[:4], uint32(time.Now().Unix()))
			_, _ = rand.Read(b[4:])
			traceID = hex.EncodeToString(b)
		}

		if spanID == "" {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			spanID = hex.EncodeToString(b)
		}

		log = log.With("trace_id", traceID, "span_id", spanID)
		ctx := context.WithValue(r.Context(), loggerKey, log)

		if err := next(w, r.WithContext(ctx)); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			log.Error("bad request", "error", err)
		}
	}
}

func (s *Server) logger(ctx context.Context) *slog.Logger {
	if log, ok := ctx.Value(loggerKey).(*slog.Logger); ok {
		return log
	}
	return s.log.Logger
}

func (s *Server) post(next handlerFuncWithBody) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return fmt.Errorf("method %s not allowed", r.Method)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			return fmt.Errorf("read body error: %w", err)
		}
		_ = r.Body.Close()

		return next(w, body)
	}
}

func (s *Server) response(w http.ResponseWriter, resp proto.Message) error {
	data, err := proto.Marshal(resp)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/x-protobuf")

	if _, err = w.Write(data); err != nil {
		return fmt.Errorf("response write error: %w", err)
	}

	return nil
}
