package api

import (
	"sync"
	"testing"
	"time"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

// Eviction is the only broadcast path that mutates the client map; guards
// against doing so under a read lock while other goroutines read the map.
func TestHub_SlowClientEviction(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	const clients = 20
	for i := 0; i < clients; i++ {
		// send is never drained, so it fills up and the client is evicted.
		hub.register <- &Client{send: make(chan []byte, 1), hub: hub, filters: map[string]bool{}}
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				hub.ConnectedClients()
			}
		}
	}()

	for i := 0; i < 50; i++ {
		hub.broadcast <- models.WebSocketEvent{Type: "test"}
	}

	deadline := time.Now().Add(5 * time.Second)
	for hub.ConnectedClients() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	close(stop)
	wg.Wait()

	if got := hub.ConnectedClients(); got != 0 {
		t.Fatalf("slow clients not evicted: %d remain", got)
	}
}
