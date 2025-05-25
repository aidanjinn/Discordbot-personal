package bot

import (
	"context"
	"github.com/bwmarrin/discordgo"
	"log"
	"math/rand"
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
	discord.ChannelMessageSend(message.ChannelID, "Attempting Connection...")
	guildID := message.GuildID

	if botIsInSameVoiceChannel(discord, guildID, message.Author.ID) {
		discord.ChannelMessageSend(message.ChannelID, "Already in Channel...")
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

	discord.ChannelMessageSend(message.ChannelID, "✅ Connected to Voice Channel")
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
