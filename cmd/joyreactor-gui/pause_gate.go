package main

import (
	"context"
	"sync"
)

// pauseGate is a reusable pause/resume primitive. When not paused, Wait returns
// immediately. When paused, Wait blocks until Resume is called or ctx cancels.
type pauseGate struct {
	mu sync.Mutex
	ch chan struct{} // closed = not paused; open = paused
}

func newPauseGate() *pauseGate {
	closed := make(chan struct{})
	close(closed)
	return &pauseGate{ch: closed}
}

func (g *pauseGate) IsPaused() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	select {
	case <-g.ch:
		return false
	default:
		return true
	}
}

func (g *pauseGate) Pause() {
	g.mu.Lock()
	defer g.mu.Unlock()
	select {
	case <-g.ch:
		g.ch = make(chan struct{})
	default:
		// already paused
	}
}

func (g *pauseGate) Resume() {
	g.mu.Lock()
	defer g.mu.Unlock()
	select {
	case <-g.ch:
		// already running
	default:
		close(g.ch)
	}
}

// Wait blocks while paused. Returns ctx.Err() on cancellation.
func (g *pauseGate) Wait(ctx context.Context) error {
	g.mu.Lock()
	ch := g.ch
	g.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
