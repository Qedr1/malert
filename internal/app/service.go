package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"alerting/internal/clock"
	"alerting/internal/config"
	"alerting/internal/ingest"
	"alerting/internal/logging"
	"alerting/internal/notify"
	"alerting/internal/notifyqueue"
	"alerting/internal/state"
)

// Service composes runtime dependencies and process lifecycle.
// Params: config source and shared runtime components.
// Returns: runnable alerting service.
type Service struct {
	source    config.ConfigSource
	cfg       config.Config
	logger    *slog.Logger
	closeLog  func()
	store     state.Store
	manager   *Manager
	httpSrv   *http.Server
	natsSub   interface{ Close() error }
	deleteSub interface{ Close() error }
	notifyQ   interface{ Close() error }
	notifyPub notifyqueue.Producer
	readyFlag atomic.Bool
	clock     clock.Clock
}

// NewService builds service instance from config source.
// Params: config source and clock implementation.
// Returns: initialized service or setup error.
func NewService(source config.ConfigSource, clk clock.Clock) (*Service, error) {
	cfg, err := config.LoadSnapshot(source)
	if err != nil {
		return nil, err
	}

	logger, closeLog, err := logging.New(cfg.Log)
	if err != nil {
		return nil, err
	}

	store, err := buildStore(cfg)
	if err != nil {
		closeLog()
		return nil, err
	}

	dispatcher := notify.NewDispatcher(cfg.Notify, logger)
	manager := NewManager(cfg, logger, store, dispatcher, clk)

	service := &Service{
		source:   source,
		cfg:      cfg,
		logger:   logger,
		closeLog: closeLog,
		store:    store,
		manager:  manager,
		clock:    clk,
	}

	if err := service.buildHTTPServer(); err != nil {
		service.cleanupInitResources()
		return nil, err
	}
	if err := service.buildNATSSubscriber(); err != nil {
		service.cleanupInitResources()
		return nil, err
	}
	if err := service.buildDeleteConsumer(); err != nil {
		service.cleanupInitResources()
		return nil, err
	}
	if err := service.buildNotifyQueue(); err != nil {
		service.cleanupInitResources()
		return nil, err
	}

	return service, nil
}

// Run starts service lifecycle and blocks until shutdown signal.
// Params: root context for service runtime.
// Returns: terminal run error.
func (s *Service) Run(ctx context.Context) error {
	shutdownCtx, shutdownCancel := context.WithCancel(ctx)
	defer shutdownCancel()

	errChan := make(chan error, 1)
	go func() {
		s.logger.Info("http server starting", "listen", s.cfg.Ingest.HTTP.Listen)
		err := s.httpSrv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errChan <- err
		}
	}()

	tickInterval := time.Duration(s.cfg.Service.ResolveScanInterval) * time.Second
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	go func() {
		for {
			select {
			case <-shutdownCtx.Done():
				return
			case <-ticker.C:
				if err := s.manager.Tick(context.Background()); err != nil {
					s.logger.Error("tick processing failed", "error", err.Error())
				}
			}
		}
	}()

	if s.cfg.Service.ReloadEnabled {
		reloadInterval := time.Duration(s.cfg.Service.ReloadIntervalSec) * time.Second
		reloadTicker := time.NewTicker(reloadInterval)
		defer reloadTicker.Stop()
		go func() {
			for {
				select {
				case <-shutdownCtx.Done():
					return
				case <-reloadTicker.C:
					if err := s.reloadConfig(context.Background()); err != nil {
						s.logger.Error("reload failed", "error", err.Error())
					}
				}
			}
		}()
	}

	s.readyFlag.Store(true)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	select {
	case <-ctx.Done():
		return s.shutdown()
	case err := <-errChan:
		_ = s.shutdown()
		return fmt.Errorf("http server failed: %w", err)
	case <-sigChan:
		return s.shutdown()
	}
}

// shutdown closes runtime resources in dependency order.
// Params: none.
// Returns: first close error.
func (s *Service) shutdown() error {
	s.readyFlag.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := s.httpSrv.Shutdown(ctx); err != nil {
		s.logger.Error("http shutdown failed", "error", err.Error())
	}
	if s.natsSub != nil {
		if err := s.natsSub.Close(); err != nil {
			s.logger.Error("nats subscriber close failed", "error", err.Error())
		}
	}
	if s.deleteSub != nil {
		if err := s.deleteSub.Close(); err != nil {
			s.logger.Error("delete-marker consumer close failed", "error", err.Error())
		}
	}
	if s.notifyQ != nil {
		if err := s.notifyQ.Close(); err != nil {
			s.logger.Error("notify queue worker close failed", "error", err.Error())
		}
	}
	if s.notifyPub != nil {
		if err := s.notifyPub.Close(); err != nil {
			s.logger.Error("notify queue producer close failed", "error", err.Error())
		}
	}
	if err := s.store.Close(); err != nil {
		s.logger.Error("store close failed", "error", err.Error())
	}
	if s.closeLog != nil {
		s.closeLog()
	}
	return nil
}

// cleanupInitResources closes partially initialized resources on startup failures.
// Params: none.
// Returns: all acquired resources closed best-effort.
func (s *Service) cleanupInitResources() {
	if s.deleteSub != nil {
		_ = s.deleteSub.Close()
		s.deleteSub = nil
	}
	if s.notifyQ != nil {
		_ = s.notifyQ.Close()
		s.notifyQ = nil
	}
	if s.notifyPub != nil {
		_ = s.notifyPub.Close()
		s.notifyPub = nil
	}
	if s.natsSub != nil {
		_ = s.natsSub.Close()
		s.natsSub = nil
	}
	if s.httpSrv != nil {
		_ = s.httpSrv.Close()
		s.httpSrv = nil
	}
	if s.store != nil {
		_ = s.store.Close()
		s.store = nil
	}
	if s.closeLog != nil {
		s.closeLog()
		s.closeLog = nil
	}
}

// buildHTTPServer wires router with ingest and health endpoints.
// Params: none.
// Returns: setup error.
func (s *Service) buildHTTPServer() error {
	mux := http.NewServeMux()
	mux.HandleFunc(s.cfg.Ingest.HTTP.HealthPath, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok"))
	})
	mux.HandleFunc(s.cfg.Ingest.HTTP.ReadyPath, func(writer http.ResponseWriter, _ *http.Request) {
		if !s.readyFlag.Load() {
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte("not-ready"))
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ready"))
	})

	if s.cfg.Ingest.HTTP.Enabled {
		handler := ingest.NewHTTPHandler(s.manager, s.cfg.Ingest.HTTP.MaxBodyBytes)
		mux.Handle(s.cfg.Ingest.HTTP.IngestPath, handler)
	}

	s.httpSrv = &http.Server{
		Addr:              s.cfg.Ingest.HTTP.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return nil
}

// buildNATSSubscriber starts NATS ingest when enabled.
// Params: none.
// Returns: initialization error.
func (s *Service) buildNATSSubscriber() error {
	if !s.cfg.Ingest.NATS.Enabled {
		return nil
	}
	subscriber, err := ingest.NewNATSSubscriber(s.cfg.Ingest.NATS, s.manager, s.logger)
	if err != nil {
		return err
	}
	s.natsSub = subscriber
	return nil
}

// buildDeleteConsumer starts queue consumer for tick delete markers.
// Params: none.
// Returns: initialization error when consumer cannot be started.
func (s *Service) buildDeleteConsumer() error {
	stateCfg := config.DeriveStateNATSConfig(s.cfg)
	consumer, err := state.NewDeleteMarkerConsumer(stateCfg, func(ctx context.Context, alertID, reason string) error {
		if err := s.manager.ResolveByTTL(ctx, alertID, reason); err != nil {
			s.logger.Error("delete-marker resolve failed", "alert_id", alertID, "error", err.Error())
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.deleteSub = consumer
	return nil
}

// reloadConfig atomically reloads and applies new config snapshot.
// Params: context for cleanup operations.
// Returns: reload or apply error.
func (s *Service) reloadConfig(ctx context.Context) error {
	nextCfg, err := config.LoadSnapshot(s.source)
	if err != nil {
		return err
	}
	nextDispatcher := notify.NewDispatcher(nextCfg.Notify, s.logger)
	nextProducer, nextWorker, err := s.buildNotifyQueueRuntime(nextCfg)
	if err != nil {
		return err
	}
	if err := s.manager.ApplyConfig(ctx, nextCfg); err != nil {
		if nextWorker != nil {
			_ = nextWorker.Close()
		}
		if nextProducer != nil {
			_ = nextProducer.Close()
		}
		return err
	}
	if s.notifyQ != nil {
		_ = s.notifyQ.Close()
	}
	if s.notifyPub != nil {
		_ = s.notifyPub.Close()
	}
	s.notifyQ = nextWorker
	s.notifyPub = nextProducer
	s.manager.SetDispatcher(nextDispatcher)
	s.manager.SetQueueProducer(nextProducer)
	s.cfg = nextCfg
	s.logger.Info("configuration reloaded")
	return nil
}

// buildNotifyQueue initializes async notification producer+worker when enabled.
// Params: none.
// Returns: setup error.
func (s *Service) buildNotifyQueue() error {
	producer, worker, err := s.buildNotifyQueueRuntime(s.cfg)
	if err != nil {
		return err
	}
	s.notifyPub = producer
	s.notifyQ = worker
	s.manager.SetQueueProducer(producer)
	return nil
}

// buildNotifyQueueRuntime creates queue producer/worker pair from config snapshot.
// Params: config snapshot.
// Returns: producer and worker handles (nil when queue disabled).
func (s *Service) buildNotifyQueueRuntime(cfg config.Config) (notifyqueue.Producer, interface{ Close() error }, error) {
	if !cfg.Notify.Queue.Enabled {
		return nil, nil, nil
	}
	producer, err := notifyqueue.NewNATSProducer(cfg.Notify.Queue)
	if err != nil {
		return nil, nil, err
	}
	worker, err := notifyqueue.NewNATSWorker(cfg.Notify.Queue, s.logger, func(ctx context.Context, job notifyqueue.Job) error {
		return s.manager.ProcessQueuedNotification(ctx, job)
	})
	if err != nil {
		_ = producer.Close()
		return nil, nil, err
	}
	return producer, worker, nil
}

// buildStore creates runtime state backend from config.
// Params: root config snapshot.
// Returns: selected store backend.
func buildStore(cfg config.Config) (state.Store, error) {
	return state.NewNATSStore(config.DeriveStateNATSConfig(cfg))
}
