package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"
)

const upstreamTimeout = 10 * time.Second

type CacheData struct {
	Policy                    *Policy
	Nodes                     []Node
	ProxyHosts                []ProxyHost
	GrantRoleSelectorsByLogin map[string][]string
	ControlPlane              ControlPlaneMetadata
	UpdatedAt                 time.Time
}

type Cache struct {
	data         atomic.Pointer[CacheData]
	controlPlane ControlPlane
	npm          *NPMClient
	interval     time.Duration
	logger       *slog.Logger
}

type snapshotLoadStage string

const (
	snapshotStageHeadscalePolicy  snapshotLoadStage = "Headscale policy"
	snapshotStageHeadscaleNodes   snapshotLoadStage = "Headscale nodes"
	snapshotStageTailscaleOAuth   snapshotLoadStage = "Tailscale OAuth"
	snapshotStageTailscalePolicy  snapshotLoadStage = "Tailscale policy"
	snapshotStageTailscaleUsers   snapshotLoadStage = "Tailscale users"
	snapshotStageTailscaleDevices snapshotLoadStage = "Tailscale devices"
	snapshotStageControlPlane     snapshotLoadStage = "control plane"
	snapshotStageNPMAuth          snapshotLoadStage = "NPM authentication"
	snapshotStageNPMProxyHosts    snapshotLoadStage = "NPM proxy hosts"
)

type snapshotLoadError struct {
	Stage snapshotLoadStage
	Err   error
}

func (e *snapshotLoadError) Error() string { return fmt.Sprintf("%s: %v", e.Stage, e.Err) }
func (e *snapshotLoadError) Unwrap() error { return e.Err }

type snapshotLoadProgress func(stage snapshotLoadStage, count int)

func NewCache(controlPlane ControlPlane, npm *NPMClient, interval time.Duration, logger *slog.Logger) *Cache {
	return &Cache{
		controlPlane: controlPlane,
		npm:          npm,
		interval:     interval,
		logger:       logger,
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

func (c *Cache) Get() *CacheData { return c.data.Load() }

func (c *Cache) LastUpdated() time.Time {
	if d := c.data.Load(); d != nil {
		return d.UpdatedAt
	}
	return time.Time{}
}

func (c *Cache) refresh(ctx context.Context) error {
	snapshot, err := loadSnapshot(ctx, c.controlPlane, c.npm)
	if err != nil {
		return fmt.Errorf("refresh: %w", err)
	}
	c.data.Store(snapshot)
	c.logger.Info("cache refreshed",
		"provider", snapshot.ControlPlane.Provider,
		"nodes", len(snapshot.Nodes), "proxy_hosts", len(snapshot.ProxyHosts))
	return nil
}

func loadSnapshot(ctx context.Context, controlPlane ControlPlane, npm *NPMClient) (*CacheData, error) {
	return loadSnapshotWithProgress(ctx, controlPlane, npm, nil)
}

func loadSnapshotWithProgress(ctx context.Context, controlPlane ControlPlane, npm *NPMClient, progress snapshotLoadProgress) (*CacheData, error) {
	if controlPlane == nil {
		return nil, &snapshotLoadError{Stage: snapshotStageControlPlane, Err: fmt.Errorf("client is unavailable")}
	}

	provider := controlPlane.Provider()
	controlResult, err := controlPlane.Load(ctx, func(stage controlPlaneLoadStage, count int) {
		reportSnapshotProgress(progress, snapshotStageForControlPlane(provider, stage), count)
	})
	if err != nil {
		stage := snapshotStageControlPlane
		var loadErr *controlPlaneLoadError
		if errors.As(err, &loadErr) {
			stage = snapshotStageForControlPlane(loadErr.Provider, loadErr.Stage)
		}
		return nil, &snapshotLoadError{Stage: stage, Err: err}
	}
	if controlResult == nil || controlResult.Policy == nil {
		return nil, &snapshotLoadError{Stage: snapshotStageControlPlane, Err: fmt.Errorf("provider returned an incomplete result")}
	}

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
		Policy:                    controlResult.Policy,
		Nodes:                     controlResult.Nodes,
		ProxyHosts:                proxyHosts,
		GrantRoleSelectorsByLogin: controlResult.GrantRoleSelectorsByLogin,
		ControlPlane:              controlResult.Metadata,
		UpdatedAt:                 time.Now(),
	}, nil
}

func snapshotStageForControlPlane(provider controlPlaneProvider, stage controlPlaneLoadStage) snapshotLoadStage {
	switch provider {
	case controlPlaneHeadscale:
		switch stage {
		case controlPlaneStagePolicy:
			return snapshotStageHeadscalePolicy
		case controlPlaneStageNodes, controlPlaneStageDevices:
			return snapshotStageHeadscaleNodes
		}
	case controlPlaneTailscale:
		switch stage {
		case controlPlaneStageAuth:
			return snapshotStageTailscaleOAuth
		case controlPlaneStagePolicy:
			return snapshotStageTailscalePolicy
		case controlPlaneStageUsers:
			return snapshotStageTailscaleUsers
		case controlPlaneStageDevices, controlPlaneStageNodes:
			return snapshotStageTailscaleDevices
		}
	}
	return snapshotStageControlPlane
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
