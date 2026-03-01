package main

import (
	"math/rand/v2"
)

type episodeEntry struct {
	Series  string
	Episode string
	Style   string
}

var episodePool = []episodeEntry{
	// Cosmos
	{"Cosmos", "Stellar Drift", "Cosmos"},
	{"Cosmos", "Rings of Saturn", "Cosmos"},
	{"Cosmos", "The Quiet Nebula", "Cosmos"},
	{"Cosmos", "Moons of Jupiter", "Cosmos"},
	{"Cosmos", "Distant Suns", "Cosmos"},
	{"Cosmos", "Velvet Horizon", "Cosmos"},
	{"Cosmos", "Frozen Comets", "Cosmos"},
	// Earthside
	{"Earthside", "Morning Meadow", "Earthside"},
	{"Earthside", "The Still Lake", "Earthside"},
	{"Earthside", "Moss and Stone", "Earthside"},
	{"Earthside", "Gentle Rain", "Earthside"},
	{"Earthside", "Moonlit Hills", "Earthside"},
	{"Earthside", "Whispering Pines", "Earthside"},
	{"Earthside", "River at Dusk", "Earthside"},
	// Myth
	{"Myth", "The Sleeping Library", "Myth"},
	{"Myth", "Lantern Garden", "Myth"},
	{"Myth", "Dreaming Mountains", "Myth"},
	{"Myth", "The Quiet Village", "Myth"},
	{"Myth", "Enchanted Grove", "Myth"},
	{"Myth", "The Ancient Well", "Myth"},
	{"Myth", "Silk and Starlight", "Myth"},
}

// pickEpisodes returns up to n entries from the pool that are not in usedEpisodes.
func pickEpisodes(n int, usedEpisodes map[string]bool) []episodeEntry {
	var available []episodeEntry
	for _, e := range episodePool {
		if !usedEpisodes[e.Episode] {
			available = append(available, e)
		}
	}
	rand.Shuffle(len(available), func(i, j int) {
		available[i], available[j] = available[j], available[i]
	})
	if n > len(available) {
		n = len(available)
	}
	return available[:n]
}
