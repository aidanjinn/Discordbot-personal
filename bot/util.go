package bot

import (
	"github.com/bwmarrin/discordgo"
	"log"
)

func gatherVoiceChannels(discord *discordgo.Session, message *discordgo.MessageCreate, guildID string) ([]string, error) {
	var voiceChannels []string
	// now we gather all the guild channels
	channels, err := discord.GuildChannels(guildID)
	if err != nil {
		log.Printf("Failed to get guild channels: %v", err)
		discord.ChannelMessageSend(message.ChannelID, "❌ Failed to get voice channels.")
		return nil, err
	}

	for _, channel := range channels {
		if channel.Type == discordgo.ChannelTypeGuildVoice {
			voiceChannels = append(voiceChannels, channel.ID)
		}
	}

	if len(voiceChannels) == 0 {
		log.Printf(message.ChannelID, "❌ No voice channels found.")
		return nil, err
	}

	return voiceChannels, nil
}

func gatherUsersVoiceStates(discord *discordgo.Session, message *discordgo.MessageCreate, guildID string, targetChannelID string) ([]*discordgo.VoiceState, error) {

	guild, err := discord.State.Guild(guildID)
	if err != nil {
		log.Printf(message.ChannelID, "❌ Failed to get guild state.")
		return nil, err
	}

	var usersInVoice []*discordgo.VoiceState
	if targetChannelID != "" {
		for _, vs := range guild.VoiceStates {
			usersInVoice = append(usersInVoice, vs)
		}
	} else {
		for _, vs := range guild.VoiceStates {
			if vs.ChannelID == targetChannelID {
				usersInVoice = append(usersInVoice, vs)
			}
		}
	}

	if len(usersInVoice) < 0 {
		log.Printf(message.ChannelID, "❌ No users in your voice channel to move.")
		return nil, err
	}

	// Success in collection of Guild Channels
	return usersInVoice, nil
}
