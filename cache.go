package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"
)

const upstreamTimeout = 10 * time.Second

type CacheData struct {
	Policy     *Policy
	Nodes      []Node
	ProxyHosts []ProxyHost
	UpdatedAt  time.Time
}

type Cache struct {
	data      atomic.Pointer[CacheData]
	headscale *HeadscaleClient
	npm       *NPMClient
	interval  time.Duration
	logger    *slog.Logger
}

type snapshotLoadStage string

const (
	snapshotStageHeadscalePolicy snapshotLoadStage = "Headscale policy"
	snapshotStageHeadscaleNodes  snapshotLoadStage = "Headscale nodes"
	snapshotStageNPMAuth         snapshotLoadStage = "NPM authentication"
	snapshotStageNPMProxyHosts   snapshotLoadStage = "NPM proxy hosts"
)

type snapshotLoadError struct {
	Stage snapshotLoadStage
	Err   error
}

func (e *snapshotLoadError) Error() string {
	return fmt.Sprintf("%s: %v", e.Stage, e.Err)
}

func (e *snapshotLoadError) Unwrap() error {
	return e.Err
}

type snapshotLoadProgress func(stage snapshotLoadStage, count int)

func NewCache(headscale *HeadscaleClient, npm *NPMClient, interval time.Duration, logger *slog.Logger) *Cache {
	return &Cache{
		headscale: headscale,
		npm:       npm,
		interval:  interval,
		logger:    logger,
	}
}

func (c *Cache) Start(ctx context.Context) {
	if err := c.refresh(ctx); err != nil {
		c.logger.Error("initial cache refresh failed", "err", err)
	}

	ticker := time.NewTicker(c.interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				c.logger.Info("cache polling stopped")
				return
			case <-ticker.C:
				if err := c.refresh(ctx); err != nil {
					c.logger.Error("cache refresh failed, keeping stale data", "err", err)
				}
			}
		}
	}()
}

func (c *Cache) Get() *CacheData {
	return c.data.Load()
}

func (c *Cache) LastUpdated() time.Time {
	if d := c.data.Load(); d != nil {
		return d.UpdatedAt
	}
	return time.Time{}
}

func (c *Cache) refresh(ctx context.Context) error {
	snapshot, err := loadSnapshot(ctx, c.headscale, c.npm)
	if err != nil {
		return fmt.Errorf("refresh: %w", err)
	}

	c.data.Store(snapshot)
	c.logger.Info("cache refreshed",
		"nodes", len(snapshot.Nodes), "proxy_hosts", len(snapshot.ProxyHosts))
	return nil
}

func loadSnapshot(ctx context.Context, headscale *HeadscaleClient, npm *NPMClient) (*CacheData, error) {
	return loadSnapshotWithProgress(ctx, headscale, npm, nil)
}

func loadSnapshotWithProgress(ctx context.Context, headscale *HeadscaleClient, npm *NPMClient, progress snapshotLoadProgress) (*CacheData, error) {
	if headscale == nil {
		return nil, &snapshotLoadError{Stage: snapshotStageHeadscalePolicy, Err: fmt.Errorf("client is unavailable")}
	}

	policy, err := call(ctx, headscale.FetchPolicy)
	if err != nil {
		return nil, &snapshotLoadError{Stage: snapshotStageHeadscalePolicy, Err: err}
	}
	reportSnapshotProgress(progress, snapshotStageHeadscalePolicy, len(policy.ACLs))

	nodes, err := call(ctx, headscale.FetchNodes)
	if err != nil {
		return nil, &snapshotLoadError{Stage: snapshotStageHeadscaleNodes, Err: err}
	}
	reportSnapshotProgress(progress, snapshotStageHeadscaleNodes, len(nodes))

	if npm == nil {
		return nil, &snapshotLoadError{Stage: snapshotStageNPMAuth, Err: fmt.Errorf("client is unavailable")}
	}
	if _, err := call(ctx, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, npm.ensureToken(ctx)
	}); err != nil {
		return nil, &snapshotLoadError{Stage: snapshotStageNPMAuth, Err: err}
	}
	reportSnapshotProgress(progress, snapshotStageNPMAuth, 0)

	proxyHosts, err := call(ctx, npm.FetchProxyHosts)
	if err != nil {
		return nil, &snapshotLoadError{Stage: snapshotStageNPMProxyHosts, Err: err}
	}
	reportSnapshotProgress(progress, snapshotStageNPMProxyHosts, len(proxyHosts))

	return &CacheData{
		Policy:     policy,
		Nodes:      nodes,
		ProxyHosts: proxyHosts,
		UpdatedAt:  time.Now(),
	}, nil
}

func reportSnapshotProgress(progress snapshotLoadProgress, stage snapshotLoadStage, count int) {
	if progress != nil {
		progress(stage, count)
	}
}

func call[T any](ctx context.Context, fn func(context.Context) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, upstreamTimeout)
	defer cancel()
	return fn(ctx)
}
