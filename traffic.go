package main

import (
	"context"
	"net"
	"sync/atomic"
	"time"
)

type trafficCounters struct {
	upload, download atomic.Uint64
}

type trafficSample struct {
	Upload, Download uint64
	UpRate, DownRate float64
}

func (t *trafficCounters) sample(previous trafficSample, elapsed time.Duration) trafficSample {
	next := trafficSample{Upload: t.upload.Load(), Download: t.download.Load()}
	if elapsed > 0 {
		next.UpRate = float64(next.Upload-previous.Upload) / elapsed.Seconds()
		next.DownRate = float64(next.Download-previous.Download) / elapsed.Seconds()
	}
	return next
}

func (t *trafficCounters) count(dial dialFunc) dialFunc {
	if dial == nil {
		return nil
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &trafficConn{Conn: conn, counters: t}, nil
	}
}

type trafficConn struct {
	net.Conn
	counters *trafficCounters
}

func (c *trafficConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.counters.download.Add(uint64(n))
	return n, err
}

func (c *trafficConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.counters.upload.Add(uint64(n))
	return n, err
}

func (c *trafficConn) CloseWrite() error {
	if conn, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return conn.CloseWrite()
	}
	return c.Close()
}
