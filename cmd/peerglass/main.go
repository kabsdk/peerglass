// Command peerglass serves the web UI for rTorrent. It is configured from
// the environment (N2) and logs JSON lines to stdout (N3).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kabsdk/peerglass/internal/rtorrent"
	"github.com/kabsdk/peerglass/internal/web"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	socket := env("PEERGLASS_SOCKET", "/run/rtorrent/rtorrent.sock")
	listen := env("PEERGLASS_LISTEN", ":8080")
	root := env("PEERGLASS_ROOT", "/downloads/")
	labels := splitLabels(env("PEERGLASS_LABELS", "tv,movies"))

	client := rtorrent.New(socket)
	handler, err := web.New(client, web.Config{Labels: labels, Root: root}, logger)
	if err != nil {
		logger.Error("startup", "err", err.Error())
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute, // uploads of several .torrent files
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown", "err", err.Error())
		}
	}()

	// rTorrent may well be down at startup; the UI copes (F11), so this
	// is information, not a condition.
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	if v, err := client.Version(probe); err != nil {
		logger.Warn("rtorrent not reachable yet", "socket", socket, "err", err.Error())
	} else {
		logger.Info("rtorrent", "version", v)
	}
	cancel()

	logger.Info("listening", "addr", listen, "socket", socket, "root", root, "labels", labels)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("serve", "err", err.Error())
		os.Exit(1)
	}
	logger.Info("stopped")
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return def
}

// splitLabels turns "tv, movies" into ["tv", "movies"], dropping blanks and
// duplicates.
func splitLabels(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range strings.Split(s, ",") {
		l = strings.TrimSpace(l)
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}
