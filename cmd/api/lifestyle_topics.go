package main

import (
	"math/rand/v2"
)

// LifestyleTopic defines a Reels content topic.
type LifestyleTopic struct {
	Category  string   // "Wellness", "Productivity", etc.
	Topic     string   // episode name / title
	BRollTags []string // Pexels search terms for B-roll
}

var lifestyleTopicPool = []LifestyleTopic{
	// Wellness
	{"Wellness", "Morning Skincare Routine", []string{"skincare", "bathroom mirror", "morning routine"}},
	{"Wellness", "5 Habits for Better Sleep", []string{"bedroom", "cozy night", "sleep routine"}},
	{"Wellness", "Hydration Tips You Need", []string{"water bottle", "fresh water", "healthy lifestyle"}},
	{"Wellness", "Mindful Breathing Exercise", []string{"meditation", "breathing", "calm nature"}},
	{"Wellness", "Weekly Self-Care Checklist", []string{"self care", "spa day", "journaling"}},
	{"Wellness", "Digital Detox Weekend", []string{"nature walk", "reading book", "sunset"}},
	{"Wellness", "Gut Health Basics", []string{"healthy food", "smoothie", "kitchen cooking"}},
	{"Wellness", "Stretching Before Bed", []string{"yoga stretching", "bedroom", "relaxation"}},

	// Productivity
	{"Productivity", "5 AM Club Secrets", []string{"alarm clock", "sunrise", "morning coffee"}},
	{"Productivity", "Deep Work in 25 Minutes", []string{"desk work", "typing laptop", "focus study"}},
	{"Productivity", "Sunday Reset Routine", []string{"cleaning home", "organizing", "planner"}},
	{"Productivity", "How I Plan My Week", []string{"planner notebook", "desk setup", "calendar"}},
	{"Productivity", "Stop Procrastinating Now", []string{"motivation", "working laptop", "time management"}},
	{"Productivity", "My Minimal Desk Setup", []string{"minimal desk", "workspace", "aesthetic setup"}},
	{"Productivity", "3 Apps That Changed My Life", []string{"phone apps", "technology", "productivity"}},
	{"Productivity", "Evening Shutdown Routine", []string{"closing laptop", "evening sky", "journaling"}},

	// Self-Care
	{"Self-Care", "Nighttime Wind-Down Ritual", []string{"candle", "herbal tea", "cozy blanket"}},
	{"Self-Care", "Journaling for Beginners", []string{"journal writing", "notebook pen", "coffee morning"}},
	{"Self-Care", "Saying No Without Guilt", []string{"confident woman", "boundary", "peaceful"}},
	{"Self-Care", "Solo Date Ideas", []string{"cafe alone", "bookstore", "park walk"}},
	{"Self-Care", "How to Rest Without Guilt", []string{"relaxing couch", "reading", "peaceful afternoon"}},
	{"Self-Care", "Boundaries That Changed My Life", []string{"peaceful nature", "strong confident", "morning light"}},
	{"Self-Care", "My Favorite Comfort Rituals", []string{"hot chocolate", "blanket cozy", "rainy window"}},

	// Fitness
	{"Fitness", "10-Minute Morning Stretch", []string{"yoga stretching", "morning exercise", "fitness indoor"}},
	{"Fitness", "Walking Changed My Body", []string{"walking park", "nature trail", "outdoor exercise"}},
	{"Fitness", "Post-Workout Recovery Tips", []string{"gym recovery", "stretching", "healthy smoothie"}},
	{"Fitness", "Desk Exercises for WFH", []string{"office exercise", "desk stretching", "home office"}},
	{"Fitness", "Why I Quit the Gym", []string{"outdoor exercise", "home workout", "nature fitness"}},

	// Mindset
	{"Mindset", "Reframe Negative Thoughts", []string{"meditation", "peaceful sunrise", "journal writing"}},
	{"Mindset", "The 2-Minute Rule", []string{"productivity", "quick task", "motivation"}},
	{"Mindset", "How I Handle Bad Days", []string{"rainy window", "cozy blanket", "tea cup"}},
	{"Mindset", "Gratitude Practice That Works", []string{"journal gratitude", "sunrise", "thankful"}},
	{"Mindset", "Stop Comparing Yourself", []string{"mirror reflection", "confident", "self love"}},
	{"Mindset", "Tiny Habits, Big Changes", []string{"small steps", "progress", "daily routine"}},

	// Home & Lifestyle
	{"Home", "Aesthetic Room Makeover", []string{"room decor", "minimalist room", "interior design"}},
	{"Home", "Cozy Evening Ambiance", []string{"candle light", "fairy lights", "cozy room"}},
	{"Home", "Kitchen Organization Hacks", []string{"kitchen organizing", "clean kitchen", "storage"}},
	{"Home", "My Morning Coffee Ritual", []string{"coffee making", "espresso", "morning light"}},
	{"Home", "Plants That Purify Air", []string{"indoor plants", "green home", "plant care"}},

	// Finance
	{"Finance", "Money Saving Challenge", []string{"piggy bank", "saving money", "finance planning"}},
	{"Finance", "Budget Like a Pro", []string{"calculator", "budget planning", "notebook finance"}},
	{"Finance", "Stop Impulse Buying", []string{"shopping", "credit card", "minimalism"}},

	// Recipes
	{"Recipes", "3-Ingredient Smoothie Bowl", []string{"smoothie bowl", "healthy breakfast", "fruit bowl"}},
	{"Recipes", "Easy Meal Prep Sunday", []string{"meal prep", "cooking kitchen", "food containers"}},
	{"Recipes", "Overnight Oats 3 Ways", []string{"overnight oats", "breakfast jar", "healthy meal"}},
}

// pickLifestyleTopics returns up to n topics not in usedTopics.
func pickLifestyleTopics(n int, usedTopics map[string]bool) []LifestyleTopic {
	var available []LifestyleTopic
	for _, t := range lifestyleTopicPool {
		if !usedTopics[t.Topic] {
			available = append(available, t)
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
