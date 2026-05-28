package hubsync

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"m31labs.dev/gosx/crdt"
	crdtsync "m31labs.dev/gosx/crdt/sync"
	"m31labs.dev/hyphae/internal/crdtshadow"
)

// PullStats describes one sync pass for reporting.
type PullStats struct {
	BytesSent          int      `json:"bytes_sent"`
	BytesReceived      int      `json:"bytes_received"`
	FramesSent         int      `json:"frames_sent"`
	FramesRecv         int      `json:"frames_received"`
	ChangesBefore      int      `json:"changes_before"`
	ChangesAfter       int      `json:"changes_after"`
	Once               bool     `json:"once"`
	MaterializedFiles  []string `json:"materialized_files,omitempty"`
}

// Pull opens a websocket connection to the peer's hub for spaceURI,
// exchanges sync messages, and either returns after one full pass
// (once=true) or runs until ctx is cancelled.
//
// peerURL must be a base URL like "ws://host:7777" — Pull appends
// the per-space path. token, when non-empty, is sent as a Bearer
// token in the upgrade request.
func Pull(ctx context.Context, peerURL, spaceURI, token string, sh *crdtshadow.Shadow, once bool) (PullStats, error) {
	endpoint := EndpointURL(peerURL, spaceURI)
	uri, err := url.Parse(endpoint)
	if err != nil {
		return PullStats{}, fmt.Errorf("hubsync: parse %s: %w", endpoint, err)
	}
	// Normalise scheme: http(s) → ws(s).
	switch uri.Scheme {
	case "http":
		uri.Scheme = "ws"
	case "https":
		uri.Scheme = "wss"
	case "ws", "wss":
		// already correct
	default:
		return PullStats{}, fmt.Errorf("hubsync: unsupported scheme %q", uri.Scheme)
	}

	hdr := http.Header{}
	if token != "" {
		hdr.Set("Authorization", "Bearer "+token)
	}

	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 10 * time.Second
	conn, _, err := dialer.DialContext(ctx, uri.String(), hdr)
	if err != nil {
		return PullStats{}, fmt.Errorf("hubsync: dial %s: %w", uri.Redacted(), err)
	}
	defer conn.Close()

	stats := PullStats{Once: once}
	beforeCount, _ := sh.Store().CountChanges()
	stats.ChangesBefore = beforeCount

	// Shared state for one-shot synchronization. The server uses prefix
	// byte 1 because SyncDocName is registered first; clients send the
	// same prefix.
	const syncPrefix byte = 1
	state := crdtsync.NewState()

	// Background writer pump.
	var writerWG sync.WaitGroup
	writeCh := make(chan []byte, 32)
	writerWG.Add(1)
	go func() {
		defer writerWG.Done()
		for msg := range writeCh {
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.BinaryMessage, msg); err != nil {
				return
			}
			stats.BytesSent += len(msg)
			stats.FramesSent++
		}
	}()

	send := func() bool {
		raw, ok := sh.Doc().GenerateSyncMessage(state)
		if !ok || raw == nil {
			return false
		}
		framed := append([]byte{syncPrefix}, raw...)
		select {
		case writeCh <- framed:
			return true
		case <-ctx.Done():
			return false
		}
	}

	// Always push our state at the start.
	send()

	for {
		if err := ctx.Err(); err != nil {
			break
		}
		_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		mt, data, err := conn.ReadMessage()
		if err != nil {
			// Graceful close = no error to surface.
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				break
			}
			if ctx.Err() != nil {
				break
			}
			close(writeCh)
			writerWG.Wait()
			return stats, fmt.Errorf("hubsync: read: %w", err)
		}
		if mt != websocket.BinaryMessage || len(data) < 2 {
			continue
		}
		if data[0] != syncPrefix {
			// JSON frame or different doc prefix — ignore in v0.2.
			continue
		}
		stats.BytesReceived += len(data)
		stats.FramesRecv++
		if err := sh.Doc().ReceiveSyncMessage(state, data[1:]); err != nil {
			close(writeCh)
			writerWG.Wait()
			return stats, fmt.Errorf("hubsync: receive: %w", err)
		}
		_, _ = sh.Store().AppendChangesFromDoc(sh.Doc())

		// React with any new changes we now have.
		if !send() && once {
			break
		}
	}

	close(writeCh)
	writerWG.Wait()
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(time.Second))

	afterCount, _ := sh.Store().CountChanges()
	stats.ChangesAfter = afterCount

	// Land any remote canonical edits on disk by materializing each
	// changed file. Best-effort: a materialize failure is logged via
	// the returned stats but doesn't fail the pull.
	if stats.ChangesAfter > stats.ChangesBefore {
		if changed, mErr := sh.MaterializeAll(); mErr == nil {
			stats.MaterializedFiles = changed
		}
	}
	return stats, nil
}

// SchemeForBase converts an http/https base URL to the ws/wss form
// suitable for EndpointURL. Convenience for tests + the CLI.
func SchemeForBase(base string) string {
	if strings.HasPrefix(base, "http://") {
		return "ws://" + strings.TrimPrefix(base, "http://")
	}
	if strings.HasPrefix(base, "https://") {
		return "wss://" + strings.TrimPrefix(base, "https://")
	}
	return base
}

var _ = crdt.Root // keep the crdt import even when only types used elsewhere
