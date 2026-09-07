package monitor

import (
	"context"
	"log"
	"net/http"
	"time"

	"llm-benchmarker/internal/db"
)

func StartPoller(database *db.Database, ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	client := &http.Client{Timeout: 2 * time.Second}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("poller: panic recovered: %v", r)
			}
		}()
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				return
			case <-ticker.C:
				providers, err := database.GetProviders()
				if err != nil {
					continue
				}

				for _, p := range providers {
					go func(prov db.Provider) {
						defer func() {
							if r := recover(); r != nil {
								log.Printf("poller: health check panic for %s: %v", prov.URL, r)
							}
						}()
						resp, err := client.Get(prov.URL)
						status := "online"
						if err != nil {
							status = "offline"
						} else if resp.StatusCode >= 500 {
							resp.Body.Close()
							status = "offline"
						} else {
							resp.Body.Close()
						}

						database.UpdateProviderStatus(prov.ID, status)
					}(p)
				}
			}
		}
	}()
	log.Println("Background multi-host monitor started.")
}
