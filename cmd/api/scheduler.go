package main

import (
	"context"
	"log"
	"time"

	"sleepy/internal/db"
	"sleepy/internal/jobs"
)

const (
	schedulerHour   = 22 // 10 PM EST
	schedulerMinute = 0
	schedulerStyle  = "Cosmos"
)

// startScheduler runs a background goroutine that creates a daily Cosmos run
// at 22:00 America/New_York time.
func startScheduler(ctx context.Context, store *db.DB) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		log.Printf("scheduler: failed to load timezone: %v (disabled)", err)
		return
	}

	log.Printf("scheduler: started, daily %s at %02d:%02d EST", schedulerStyle, schedulerHour, schedulerMinute)

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	var lastTriggeredDate string

	for {
		select {
		case <-ctx.Done():
			log.Println("scheduler: stopped")
			return
		case <-ticker.C:
			now := time.Now().In(loc)
			today := now.Format("2006-01-02")

			// Already triggered today?
			if today == lastTriggeredDate {
				continue
			}

			// Not yet time?
			if now.Hour() != schedulerHour || now.Minute() != schedulerMinute {
				continue
			}

			// Duplicate guard: check DB.
			exists, err := store.HasRunToday(ctx, schedulerStyle)
			if err != nil {
				log.Printf("scheduler: db check failed: %v", err)
				continue
			}
			if exists {
				log.Printf("scheduler: %s run already exists for today, skipping", schedulerStyle)
				lastTriggeredDate = today
				continue
			}

			// Pick a Cosmos episode not recently used.
			used := map[string]bool{}
			episodes := pickEpisodes(1, used)
			if len(episodes) == 0 {
				log.Println("scheduler: no available episodes")
				continue
			}
			ep := episodes[0]

			run, err := store.CreateRun(ctx, ep.Series, ep.Episode, ep.Style, "en", 30)
			if err != nil {
				log.Printf("scheduler: create run failed: %v", err)
				continue
			}

			if err := store.EnqueueJob(ctx, run.ID, jobs.JobTypeRunPipeline); err != nil {
				log.Printf("scheduler: enqueue job failed: %v", err)
				continue
			}

			lastTriggeredDate = today
			log.Printf("scheduler: created daily %s run %s — %s", schedulerStyle, run.ID, ep.Episode)
		}
	}
}
