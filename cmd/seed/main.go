package main

import (
	"context"
	"flag"
	"log"
	"os"

	"sleepy/internal/db"
	"sleepy/internal/jobs"
)

func main() {
	series := flag.String("series", "Cosmos", "Series name")
	episode := flag.String("episode", "", "Episode name (random if empty)")
	style := flag.String("style", "", "Style (defaults to series)")
	lang := flag.String("lang", "en", "Language")
	dur := flag.Int("duration", 30, "Duration in minutes")
	flag.Parse()

	if *style == "" {
		*style = *series
	}
	if *episode == "" {
		*episode = *series + " Episode"
	}

	pgDSN := os.Getenv("PG_DSN")
	if pgDSN == "" {
		pgDSN = "postgres://localhost:5432/sleepy?sslmode=disable"
	}

	database, err := db.Open(pgDSN)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	run, err := database.CreateRun(ctx, *series, *episode, *style, *lang, *dur)
	if err != nil {
		log.Fatalf("create run: %v", err)
	}

	if err := database.EnqueueJob(ctx, run.ID, jobs.JobTypeRunPipeline); err != nil {
		log.Fatalf("enqueue job: %v", err)
	}

	log.Printf("created run %s — %s / %s (%s, %dmin)", run.ID, *series, *episode, *style, *dur)
}
