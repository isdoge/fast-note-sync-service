package app

import "time"

// wsHeartbeat tracks a single grace period after the liveness timeout.
type wsHeartbeat struct {
	graceAt time.Time
}

func (h *wsHeartbeat) expired(now, lastSeen time.Time, wait, interval time.Duration) bool {
	if now.Sub(lastSeen) <= wait || lastSeen.After(h.graceAt) {
		h.graceAt = time.Time{}
	}
	if now.Sub(lastSeen) <= wait {
		return false
	}
	if h.graceAt.IsZero() {
		h.graceAt = now
		return false
	}
	return now.Sub(h.graceAt) >= interval
}

// heartbeatReadDeadline leaves room for the timeout check and its probe interval.
// Read deadlines must not bypass PingLoop's grace period; write deadlines are separate.
func (w *WebsocketServer) heartbeatReadDeadline() time.Time {
	return time.Now().Add((w.config.PingWait + 3*w.config.PingInterval) * time.Second)
}
