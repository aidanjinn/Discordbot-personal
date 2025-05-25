package bot

import (
	"context"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"github.com/hraban/opus"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"
)

var BotToken string
var voiceConnections = make(map[string]*discordgo.VoiceConnection)
var encoder *opus.Encoder

var tempFiles = make(map[string][]string) // guildID -> list of temp files
var tempFilesMu sync.RWMutex

var activeOperations = make(map[string]*OperationContext)
var operationsMu sync.RWMutex

type BotManager struct {
	voiceConnections map[string]*VoiceSession
	mu               sync.RWMutex
}

type VoiceSession struct {
	connection *discordgo.VoiceConnection
	ctx        context.Context
	cancel     context.CancelFunc
	isPlaying  bool
	mu         sync.RWMutex
}

var botManager = &BotManager{
	voiceConnections: make(map[string]*VoiceSession),
}

type OperationContext struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func checkNilErr(e error) {
	if e != nil {
		log.Fatal(e)
	}
}

func newMessage(discord *discordgo.Session, message *discordgo.MessageCreate) {
	if message.Author == nil || message.Author.ID == discord.State.User.ID {
		return
	}

	guildID := message.GuildID

	switch {
	case strings.Contains(message.Content, "!help"):
		commandList := "\t!help -> command list\n\t!cum -> this is just a custom sound play\n\t!play -> play 'filename.mp3' : Heyooo.mp3 and Lorenzofuckingdies.mp3\n\t!connect\n\t!disconnect\n\t!ask -> ask Gemini AI\n\t!say -> text-to-speech\n\t!ytplay -> play YouTube audio\n\t!shuffle -> shuffle users in voice channels\n\t!kill -> stop all current bot actions"
		discord.ChannelMessageSend(message.ChannelID, "Command List:\n"+commandList)

	case strings.Contains(message.Content, "!kill"):
		killGuildOperations(guildID)
		discord.ChannelMessageSend(message.ChannelID, "🛑 Killed all active operations for this server.")

	case strings.Contains(message.Content, "!shuffle"):
		shuffleVoiceChannels(discord, message)

	case strings.Contains(message.Content, "!cum"):
		go func() {
			botConnect(discord, message)
			customMsg := &discordgo.MessageCreate{
				Message: &discordgo.Message{
					Content:   "!play Lorenzofuckingdies.mp3",
					ChannelID: message.ChannelID,
					GuildID:   message.GuildID,
				},
			}
			soundPlay(discord, customMsg)
		}()

	case strings.HasPrefix(message.Content, "!play "):
		go func() {
			botConnect(discord, message)
			soundPlay(discord, message)
		}()

	case strings.Contains(message.Content, "!connect"):
		go func() {
			botConnect(discord, message)
			customMsg := &discordgo.MessageCreate{
				Message: &discordgo.Message{
					Content:   "!play Heyooo.mp3",
					ChannelID: message.ChannelID,
					GuildID:   message.GuildID,
				},
			}
			soundPlay(discord, customMsg)
		}()

	case strings.Contains(message.Content, "!disconnect"):
		botManager.mu.Lock()
		if session, ok := botManager.voiceConnections[guildID]; ok && session != nil {
			session.cancel()
			if session.connection != nil {
				session.connection.Disconnect()
			}
			delete(botManager.voiceConnections, guildID)
			discord.ChannelMessageSend(message.ChannelID, "Good Bye 👋")
		} else {
			discord.ChannelMessageSend(message.ChannelID, "I'm not connected to a voice channel in this guild.")
		}
		botManager.mu.Unlock()

	case strings.HasPrefix(message.Content, "!say "):
		go func() {
			text := strings.TrimPrefix(message.Content, "!say ")
			filename := fmt.Sprintf("output_%d_%d.mp3", time.Now().Unix(), rand.Intn(10000))

			opID := fmt.Sprintf("tts_%s_%d", guildID, time.Now().Unix())
			ctx := createOperationContext(opID)
			defer removeOperationContext(opID)

			log.Printf("Starting TTS synthesis for file: %s", filename)
			err := synthesizeToMP3(ctx, text, filename)
			if err != nil {
				if ctx.Err() != nil {
					discord.ChannelMessageSend(message.ChannelID, "❌ TTS operation cancelled.")
				} else {
					discord.ChannelMessageSend(message.ChannelID, "❌ TTS failed: "+err.Error())
					log.Printf("TTS error: %v", err)
				}
				return
			}

			// Wait for file to be ready before proceeding
			if err := waitForfileReady(filename, 10*time.Second); err != nil {
				discord.ChannelMessageSend(message.ChannelID, "❌ TTS file not ready: "+err.Error())
				os.Remove(filename)
				return
			}

			// Add to temp file tracking
			addTempFile(guildID, filename)

			if !botConnect(discord, message) {
				removeTempFile(guildID, filename)
				return
			}

			botManager.mu.RLock()
			session, ok := botManager.voiceConnections[guildID]
			botManager.mu.RUnlock()

			if !ok || session == nil || session.connection == nil {
				discord.ChannelMessageSend(message.ChannelID, "❌ Bot is not connected to a voice channel.")
				removeTempFile(guildID, filename)
				return
			}

			log.Printf("Playing TTS file: %s", filename)
			playMP3(session, filename, discord, message.ChannelID)
			removeTempFile(guildID, filename)
		}()

	case strings.HasPrefix(message.Content, "!ask "):
		go func() {
			discord.ChannelMessageSend(message.ChannelID, "🤖 Thinking...")

			text := strings.TrimPrefix(message.Content, "!ask ")

			opID := fmt.Sprintf("gemini_%s_%d", guildID, time.Now().Unix())
			ctx := createOperationContext(opID)
			defer removeOperationContext(opID)

			log.Printf("Gemini prompt: %s", text)

			reply, err := getGeminiResponse(ctx, text)
			if err != nil {
				if ctx.Err() != nil {
					discord.ChannelMessageSend(message.ChannelID, "❌ Gemini operation cancelled.")
				} else {
					log.Printf("Gemini error: %v", err)
					discord.ChannelMessageSend(message.ChannelID, "❌ Failed to get response from Gemini: "+err.Error())
				}
				return
			}

			log.Printf("Gemini response: %s", reply)

			// Split long messages if needed (Discord has a 2000 character limit)
			if len(reply) > 2000 {
				for len(reply) > 2000 {
					discord.ChannelMessageSend(message.ChannelID, reply[:2000])
					reply = reply[2000:]
				}
			}
			discord.ChannelMessageSend(message.ChannelID, reply)

			filename := fmt.Sprintf("gemini_output_%d_%d.mp3", time.Now().Unix(), rand.Intn(10000))

			log.Printf("Starting TTS synthesis for Gemini response file: %s", filename)
			err = synthesizeToMP3(ctx, reply, filename)
			if err != nil {
				if ctx.Err() != nil {
					discord.ChannelMessageSend(message.ChannelID, "❌ TTS operation cancelled.")
				} else {
					discord.ChannelMessageSend(message.ChannelID, "❌ TTS failed: "+err.Error())
					log.Printf("TTS error for Gemini response: %v", err)
				}
				return
			}

			// Wait for file to be ready
			if err := waitForfileReady(filename, 10*time.Second); err != nil {
				discord.ChannelMessageSend(message.ChannelID, "❌ TTS file not ready: "+err.Error())
				os.Remove(filename)
				return
			}

			// Add to temp file tracking
			addTempFile(guildID, filename)

			if !botConnect(discord, message) {
				removeTempFile(guildID, filename)
				return
			}

			botManager.mu.RLock()
			session, ok := botManager.voiceConnections[guildID]
			botManager.mu.RUnlock()

			if !ok || session == nil || session.connection == nil {
				discord.ChannelMessageSend(message.ChannelID, "❌ Bot is not connected to a voice channel.")
				removeTempFile(guildID, filename)
				return
			}

			log.Printf("Playing Gemini TTS file: %s", filename)
			playMP3(session, filename, discord, message.ChannelID)
			removeTempFile(guildID, filename)
		}()

	case strings.HasPrefix(message.Content, "!ytplay "):
		go func() {
			botConnect(discord, message)
			url := strings.TrimSpace(strings.TrimPrefix(message.Content, "!ytplay "))

			opID := fmt.Sprintf("youtube_%s_%d", guildID, time.Now().Unix())
			ctx := createOperationContext(opID)
			defer removeOperationContext(opID)

			downloadAndPlayYT(ctx, discord, message.ChannelID, message.GuildID, url)
		}()
	}
}

func Run() {
	discord, err := discordgo.New("Bot " + BotToken)
	checkNilErr(err)

	discord.AddHandler(newMessage)

	err = discord.Open()
	checkNilErr(err)

	fmt.Println("Bot running....")
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c

	// Cleanup all active operations before shutdown
	killAllOperations()
	discord.Close()
}
