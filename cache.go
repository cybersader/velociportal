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
	Policy      *Policy
	Users       []User
	Nodes       []Node
	ProxyHosts  []ProxyHost
	AccessLists []AccessList
	UpdatedAt   time.Time
}

type Cache struct {
	data      atomic.Pointer[CacheData]
	headscale *HeadscaleClient
	npm       *NPMClient
	interval  time.Duration
	logger    *slog.Logger
}

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
	policy, err := call(ctx, c.headscale.FetchPolicy)
	if err != nil {
		return fmt.Errorf("refresh: policy: %w", err)
	}
	users, err := call(ctx, c.headscale.FetchUsers)
	if err != nil {
		return fmt.Errorf("refresh: users: %w", err)
	}
	nodes, err := call(ctx, c.headscale.FetchNodes)
	if err != nil {
		return fmt.Errorf("refresh: nodes: %w", err)
	}
	proxyHosts, err := call(ctx, c.npm.FetchProxyHosts)
	if err != nil {
		return fmt.Errorf("refresh: proxy hosts: %w", err)
	}
	accessLists, err := call(ctx, c.npm.FetchAccessLists)
	if err != nil {
		return fmt.Errorf("refresh: access lists: %w", err)
	}

	c.data.Store(&CacheData{
		Policy:      policy,
		Users:       users,
		Nodes:       nodes,
		ProxyHosts:  proxyHosts,
		AccessLists: accessLists,
		UpdatedAt:   time.Now(),
	})
	c.logger.Info("cache refreshed",
		"users", len(users), "nodes", len(nodes),
		"proxy_hosts", len(proxyHosts), "access_lists", len(accessLists))
	return nil
}

func call[T any](ctx context.Context, fn func(context.Context) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, upstreamTimeout)
	defer cancel()
	return fn(ctx)
}
