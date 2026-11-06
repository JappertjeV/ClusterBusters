// Upgrade-readiness-exporter: scant het OpenShift-cluster op dingen die een upgrade laten
// mislukken en zet de bevindingen als Prometheus-metrics op :8080/metrics.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	if err := run(); err != nil {
		slog.Error("exporter gestopt", "err", err)
		os.Exit(1)
	}
}

func run() error {
	interval, err := envSeconds("SCAN_INTERVAL", 60)
	if err != nil {
		return err
	}
	addr := envOr("LISTEN_ADDR", ":8080")

	cfg, err := kubeConfig()
	if err != nil {
		return err
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}
	thanos, err := newThanosClient(cfg, envOr("THANOS_URL", defaultThanosURL), serviceCAFile)
	if err != nil {
		return err
	}

	accepted, err := parseAccepted(os.Getenv("ACCEPTED_FINDINGS"))
	if err != nil {
		return err
	}
	scanner := &Scanner{
		client:          client,
		metrics:         thanos,
		includePlatform: os.Getenv("INCLUDE_PLATFORM") == "true",
		targetOverride:  os.Getenv("TARGET_VERSION"),
		accepted:        accepted,
	}

	coll := newCollector()
	reg := prometheus.NewRegistry()
	reg.MustRegister(coll, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	registerWeb(mux, coll)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go scanLoop(ctx, scanner, coll, interval)

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	slog.Info("exporter gestart", "addr", addr, "interval", interval,
		"includePlatform", scanner.includePlatform, "targetVersion", scanner.targetOverride)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func scanLoop(ctx context.Context, s *Scanner, coll *collector, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		scanCtx, cancel := context.WithTimeout(ctx, interval)
		start := time.Now()
		res := s.Scan(scanCtx)
		cancel()
		coll.set(res)

		bl, wa := 0, 0
		for _, f := range res.Findings {
			switch f.Severity {
			case Blocker:
				bl++
			case Warning:
				wa++
			}
		}
		target := "onbekend"
		if s.target != nil {
			target = s.target.String()
		}
		slog.Info("scan klaar", "duur", time.Since(start).Round(time.Millisecond), "doel", target,
			"score", res.Score, "ready", res.Ready, "blockers", bl, "warnings", wa)

		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// kubeConfig: in het cluster de ServiceAccount, lokaal ~/.kube/config van `oc login`.
func kubeConfig() (*rest.Config, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("geen in-cluster-config en geen kubeconfig: %w", err)
	}
	return cfg, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envSeconds(key string, def int) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return time.Duration(def) * time.Second, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s moet een positief aantal seconden zijn, niet %q", key, v)
	}
	return time.Duration(n) * time.Second, nil
}
