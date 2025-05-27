package main

import (
	"discord-bot/bot"
	"github.com/joho/godotenv"
	"log"
	"os"
)

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}
	value := os.Getenv("BOT_TOKEN")

	bot.BotToken = value
	bot.Run()
}
