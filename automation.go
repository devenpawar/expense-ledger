package main

import (
	"context"
	"fmt"
	"time"
)

const syncInterval = 5 * time.Minute

type SyncStatus struct {
	Running         bool       `json:"running"`
	LastAttempt     string     `json:"lastAttempt"`
	LastError       string     `json:"lastError"`
	NextSync        string     `json:"nextSync"`
	Result          SyncResult `json:"result"`
	IntervalSeconds int        `json:"intervalSeconds"`
}

func (a *App) syncStatus() SyncStatus {
	a.runtimeMu.Lock()
	defer a.runtimeMu.Unlock()
	status := a.runtime
	status.IntervalSeconds = int(syncInterval.Seconds())
	return status
}

func (a *App) performSync(ctx context.Context) (SyncResult, error) {
	if !a.syncMu.TryLock() {
		return SyncResult{}, fmt.Errorf("Gmail sync is already running")
	}
	defer a.syncMu.Unlock()
	bounded, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	a.runtimeMu.Lock()
	a.syncCancel = cancel
	a.runtime.Running = true
	a.runtime.LastAttempt = time.Now().UTC().Format(time.RFC3339)
	a.runtime.LastError = ""
	a.runtimeMu.Unlock()
	result, err := a.syncGmail(bounded)
	a.runtimeMu.Lock()
	defer a.runtimeMu.Unlock()
	a.runtime.Running = false
	a.syncCancel = nil
	a.runtime.Result = result
	a.runtime.NextSync = time.Now().Add(syncInterval).UTC().Format(time.RFC3339)
	if err != nil {
		a.runtime.LastError = err.Error()
	} else if result.LabelError != "" {
		a.runtime.LastError = result.LabelError
	}
	return result, err
}

func (a *App) queueGmail(fullSync bool) {
	if a.jobs == nil {
		return
	}
	select {
	case a.jobs <- fullSync:
	default:
	}
}

// Jobs are serialized with manual sync and label actions. Tokens are refreshed by Gmail.access.
func (a *App) runAutomation(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	if a.gmail.connected() {
		a.queueGmail(true)
	}
	for {
		fullSync := true
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case fullSync = <-a.jobs:
		}
		if !a.gmail.connected() || !a.gmail.configured() {
			continue
		}
		if fullSync {
			_, _ = a.performSync(ctx)
			continue
		}
		if !a.syncMu.TryLock() {
			continue
		}
		bounded, cancel := context.WithTimeout(ctx, time.Minute)
		a.runtimeMu.Lock()
		a.syncCancel = cancel
		a.runtimeMu.Unlock()
		err := a.flushLabels(bounded)
		cancel()
		a.runtimeMu.Lock()
		a.syncCancel = nil
		if err != nil {
			a.runtime.LastError = err.Error()
		}
		a.runtimeMu.Unlock()
		a.syncMu.Unlock()
	}
}
