package main

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

const ServiceHealthStateStale ServiceHealthState = "stale"

type serviceHealthSnapshot struct {
	Results    map[int]ServiceHealthResult
	StaleAfter time.Duration
}

type ServiceHealthStore struct {
	data atomic.Pointer[serviceHealthSnapshot]
	now  func() time.Time
}

func NewServiceHealthStore() *ServiceHealthStore {
	return &ServiceHealthStore{now: time.Now}
}

func (s *ServiceHealthStore) Get(proxyHostID int) (ServiceHealthResult, bool) {
	if s == nil {
		return ServiceHealthResult{}, false
	}
	snapshot := s.data.Load()
	if snapshot == nil {
		return ServiceHealthResult{}, false
	}
	result, ok := snapshot.Results[proxyHostID]
	if !ok {
		return ServiceHealthResult{}, false
	}
	if result.State != ServiceHealthStateUnknown &&
		result.State != ServiceHealthStateStale &&
		!result.CheckedAt.IsZero() &&
		snapshot.StaleAfter > 0 &&
		s.now().Sub(result.CheckedAt) > snapshot.StaleAfter {
		result.State = ServiceHealthStateStale
	}
	return result, true
}

func (s *ServiceHealthStore) publish(results map[int]ServiceHealthResult, staleAfter time.Duration) {
	published := make(map[int]ServiceHealthResult, len(results))
	for proxyHostID, result := range results {
		published[proxyHostID] = result
	}
	s.data.Store(&serviceHealthSnapshot{Results: published, StaleAfter: staleAfter})
}

func serviceHealthProtectedURLs(config *Config) []string {
	if config == nil {
		return nil
	}
	urls := []string{config.NPMURL}
	if config.ControlPlane == controlPlaneTailscale {
		urls = append(urls, tailscaleAPIOrigin)
	} else {
		urls = append(urls, config.HeadscaleURL)
	}
	return urls
}

type serviceHealthProber interface {
	Probe(context.Context, ProxyHost, ServiceHealthService) ServiceHealthResult
}

type serviceHealthProberFactory func(*ServiceHealthConfig) serviceHealthProber

type ServiceHealthPoller struct {
	cache         *Cache
	loader        serviceHealthConfigLoader
	newProber     serviceHealthProberFactory
	store         *ServiceHealthStore
	logger        *slog.Logger
	retryInterval time.Duration
}

func NewServiceHealthPoller(
	cache *Cache,
	loader serviceHealthConfigLoader,
	newProber serviceHealthProberFactory,
	logger *slog.Logger,
) *ServiceHealthPoller {
	if loader == nil {
		loader = serviceHealthConfigLoaderForPath("")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ServiceHealthPoller{
		cache:         cache,
		loader:        loader,
		newProber:     newProber,
		store:         NewServiceHealthStore(),
		logger:        logger,
		retryInterval: defaultServiceHealthInterval,
	}
}

func (p *ServiceHealthPoller) Store() *ServiceHealthStore {
	if p == nil {
		return nil
	}
	return p.store
}

func (p *ServiceHealthPoller) Start(ctx context.Context) {
	if p == nil {
		return
	}
	go func() {
		delay := time.Duration(0)
		for {
			if delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					if !timer.Stop() {
						<-timer.C
					}
					p.logger.Info("service health polling stopped")
					return
				case <-timer.C:
				}
			}

			if ctx.Err() != nil {
				p.logger.Info("service health polling stopped")
				return
			}
			delay = p.runCycle(ctx)
		}
	}()
}

func (p *ServiceHealthPoller) runCycle(ctx context.Context) time.Duration {
	config, err := p.loader()
	if err != nil {
		p.logger.Error("service health configuration load failed; keeping previous observations", "err", err)
		return p.retryInterval
	}
	if config == nil {
		p.logger.Error("service health configuration load failed; keeping previous observations", "err", "loader returned an incomplete result")
		return p.retryInterval
	}
	if !config.Enabled {
		p.store.publish(map[int]ServiceHealthResult{}, 0)
		return p.retryInterval
	}

	results := make(map[int]ServiceHealthResult, len(config.Services))
	for _, service := range config.Services {
		results[service.ProxyHostID] = ServiceHealthResult{
			ProxyHostID: service.ProxyHostID,
			State:       ServiceHealthStateUnknown,
		}
	}
	staleAfter := config.Interval * 3

	var snapshot *CacheData
	if p.cache != nil {
		snapshot = p.cache.Get()
	}
	if snapshot == nil || snapshot.Policy == nil || p.newProber == nil {
		p.store.publish(results, staleAfter)
		return config.Interval
	}

	proxyHosts := make(map[int]ProxyHost, len(snapshot.ProxyHosts))
	for _, proxyHost := range snapshot.ProxyHosts {
		proxyHosts[proxyHost.ID] = proxyHost
	}
	type probeJob struct {
		proxyHost ProxyHost
		service   ServiceHealthService
	}
	jobs := make([]probeJob, 0, len(config.Services))
	for _, service := range config.Services {
		proxyHost, ok := proxyHosts[service.ProxyHostID]
		if !ok || !enabledProxyHostHasSupportedDestinationMatch(proxyHost, snapshot) {
			continue
		}
		jobs = append(jobs, probeJob{proxyHost: proxyHost, service: service})
	}
	if len(jobs) == 0 {
		p.store.publish(results, staleAfter)
		return config.Interval
	}

	prober := p.newProber(config)
	if prober == nil {
		p.store.publish(results, staleAfter)
		return config.Interval
	}

	cycleCtx, cancelCycle := context.WithTimeout(ctx, config.Interval)
	defer cancelCycle()

	workers := config.Workers
	if workers > len(jobs) {
		workers = len(jobs)
	}
	jobChannel := make(chan probeJob)
	resultChannel := make(chan ServiceHealthResult)
	var workersDone sync.WaitGroup
	workersDone.Add(workers)
	for range workers {
		go func() {
			defer workersDone.Done()
			for job := range jobChannel {
				probeCtx, cancelProbe := context.WithTimeout(cycleCtx, config.Timeout)
				result := prober.Probe(probeCtx, job.proxyHost, job.service)
				cancelProbe()
				select {
				case resultChannel <- result:
				case <-cycleCtx.Done():
					return
				}
			}
		}()
	}

	go func() {
		defer close(jobChannel)
		for _, job := range jobs {
			select {
			case jobChannel <- job:
			case <-cycleCtx.Done():
				return
			}
		}
	}()
	go func() {
		workersDone.Wait()
		close(resultChannel)
	}()

	completed := 0
	for result := range resultChannel {
		if _, configured := results[result.ProxyHostID]; configured {
			results[result.ProxyHostID] = result
			completed++
		}
	}
	p.store.publish(results, staleAfter)
	p.logger.Info("service health cycle completed", "configured", len(config.Services), "eligible", len(jobs), "completed", completed)
	return config.Interval
}
