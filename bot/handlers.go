package bot

import (
	"context"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"log"
	"math/rand"
	"os"
	"strings"
	"time"
)

func botIsInSameVoiceChannel(discord *discordgo.Session, guildID, userID string) bool {
	userVoiceState, err := discord.State.VoiceState(guildID, userID)

	if userVoiceState == nil || userVoiceState.ChannelID == "" || err != nil {
		return false
	}

	botManager.mu.RLock()
	session, ok := botManager.voiceConnections[guildID]
	botManager.mu.RUnlock()

	if !ok || session == nil || session.connection == nil {
		return false
	}

	return session.connection.ChannelID == userVoiceState.ChannelID
}

func botConnect(discord *discordgo.Session, message *discordgo.MessageCreate) bool {
	guildID := message.GuildID

	if botIsInSameVoiceChannel(discord, guildID, message.Author.ID) {
		return true
	}

	if guildID == "" {
		discord.ChannelMessageSend(message.ChannelID, "This command only works in a guild.")
		return false
	}

	voiceState, err := discord.State.VoiceState(guildID, message.Author.ID)

	if err != nil {
		discord.ChannelMessageSend(message.ChannelID, "❌ Failed to get voice state.")
		return false
	}

	if voiceState == nil || voiceState.ChannelID == "" {
		discord.ChannelMessageSend(message.ChannelID, "User is not connected to a voice channel.")
		return false
	}

	vc, err := discord.ChannelVoiceJoin(guildID, voiceState.ChannelID, false, false)
	if err != nil {
		discord.ChannelMessageSend(message.ChannelID, "❌ Failed to join voice channel.")
		return false
	}

	// Create new voice session with context
	ctx, cancel := context.WithCancel(context.Background())
	session := &VoiceSession{
		connection: vc,
		ctx:        ctx,
		cancel:     cancel,
		isPlaying:  false,
	}

	botManager.mu.Lock()
	botManager.voiceConnections[guildID] = session
	botManager.mu.Unlock()

	return true
}

func shuffleVoiceChannels(discord *discordgo.Session, message *discordgo.MessageCreate) {
	guildID := message.GuildID

	// Run in goroutine to avoid blocking
	go func() {
		channels, err := discord.GuildChannels(guildID)
		if err != nil {
			discord.ChannelMessageSend(message.ChannelID, "❌ Failed to get guild channels.")
			return
		}

		var voiceChannels []string
		for _, channel := range channels {
			if channel.Type == discordgo.ChannelTypeGuildVoice {
				voiceChannels = append(voiceChannels, channel.ID)
			}
		}

		if len(voiceChannels) < 1 {
			discord.ChannelMessageSend(message.ChannelID, "❌ No voice channels found.")
			return
		}

		guild, err := discord.State.Guild(guildID)
		if err != nil {
			discord.ChannelMessageSend(message.ChannelID, "❌ Failed to get guild state.")
			return
		}

		var usersInVoice []*discordgo.VoiceState
		for _, vs := range guild.VoiceStates {
			if vs.ChannelID != "" { // Only users actually in voice channels
				usersInVoice = append(usersInVoice, vs)
			}
		}

		if len(usersInVoice) < 1 {
			discord.ChannelMessageSend(message.ChannelID, "❌ No users in voice channels to shuffle.")
			return
		}

		rand.Shuffle(len(usersInVoice), func(i, j int) {
			usersInVoice[i], usersInVoice[j] = usersInVoice[j], usersInVoice[i]
		})

		for i, vs := range usersInVoice {
			targetChannel := voiceChannels[i%len(voiceChannels)]
			err := discord.GuildMemberMove(guildID, vs.UserID, &targetChannel)
			if err != nil {
				log.Printf("Failed to move user %s: %v", vs.UserID, err)
			}
		}

		discord.ChannelMessageSend(message.ChannelID, "🔀 Shuffled users into random voice channels.")
	}()
}

func slotMachine(discord *discordgo.Session, message *discordgo.MessageCreate) {

	go func() {
		icons := map[int]string{
			0: "🍒",   // Cherries
			1: "🍋",   // Lemon
			2: "🔔",   // Bell
			3: "🍀",   // Four-leaf clover
			4: "💎",   // Diamond
			5: "7️⃣", // Lucky 7
			6: "🍇",   // Grapes
			7: "🎰",   // Slot machine
			8: "⭐",   // Star
		}
		var slots [3]int
		for i := range len(slots) {
			slots[i] = rand.Intn(9)
		}
		var output string
		output += " | "
		for _, slot := range slots {
			output += icons[slot] + " | "
		}

		ttsText := ""
		//Winner
		if slots[0] == slots[1] && slots[1] == slots[2] {
			discord.ChannelMessageSend(message.ChannelID, "You Won You Lucky Fuck\n")

			botConnect(discord, message)
			ttsText = "You Won You Lucky Fuck"

		} else {
			discord.ChannelMessageSend(message.ChannelID, "You A Fuckin Lose Dummy\n")

			botConnect(discord, message)
			ttsText = "You A Fuckin Lose Dummy"

		}

		discord.ChannelMessageSend(message.ChannelID, output)

		// Trigger "!say" behavior using same code flow
		go func() {
			filename := fmt.Sprintf("output_%d_%d.mp3", time.Now().Unix(), rand.Intn(10000))
			guildID := message.GuildID
			opID := fmt.Sprintf("tts_gamble_%s_%d", guildID, time.Now().Unix())
			ctx := createOperationContext(opID)
			defer removeOperationContext(opID)

			log.Printf("TTS for gamble result: %s", ttsText)
			err := synthesizeToMP3(ctx, ttsText, filename)
			if err != nil {
				discord.ChannelMessageSend(message.ChannelID, "❌ TTS failed: "+err.Error())
				return
			}

			if err := waitForfileReady(filename, 10*time.Second); err != nil {
				discord.ChannelMessageSend(message.ChannelID, "❌ TTS file not ready.")
				os.Remove(filename)
				return
			}

			addTempFile(guildID, filename)

			if !botConnect(discord, message) {
				removeTempFile(guildID, filename)
				return
			}

			// Play synthesized audio
			msg := &discordgo.MessageCreate{
				Message: &discordgo.Message{
					Content:   "!play " + filename,
					ChannelID: message.ChannelID,
					GuildID:   message.GuildID,
				},
			}
			soundPlay(discord, msg)

			// Clean up file after a delay
			time.AfterFunc(30*time.Second, func() {
				removeTempFile(guildID, filename)
			})
		}()
	}()
}

func newMessage(discord *discordgo.Session, message *discordgo.MessageCreate) {
	if message.Author == nil || message.Author.ID == discord.State.User.ID {
		return
	}

	guildID := message.GuildID

	switch {
	case strings.Contains(message.Content, "!help"):
		commandList := "\t!help -> command list\n\t!cum -> this is just a custom sound play\n\t" +
			"!play -> play 'filename.mp3' : Heyooo.mp3 and Lorenzofuckingdies.mp3\n\t!connect\n\t!disconnect\n\t" +
			"!ask -> ask Gemini AI\n\t!say -> text-to-speech\n\t!ytplay -> play YouTube audio\n\t!shuffle -> shuffle users in voice channels\n\t" +
			"!kill -> stop all current bot actions\n\t!gamble -> roll the slot machine"
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

			// Clean up file after a delay
			time.AfterFunc(30*time.Second, func() {
				removeTempFile(guildID, filename)
			})
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

			// Clean up file after a delay
			time.AfterFunc(30*time.Second, func() {
				removeTempFile(guildID, filename)
			})
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

	case strings.Contains(message.Content, "!gamble"):
		go func() {
			slotMachine(discord, message)
		}()
	}
}
