package bot

import (
	"encoding/json"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"log"
	"os"
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
			if vs.ChannelID == targetChannelID {
				usersInVoice = append(usersInVoice, vs)
			}
		}
	} else {
		for _, vs := range guild.VoiceStates {
			usersInVoice = append(usersInVoice, vs)
		}
	}

	if len(usersInVoice) < 0 {
		log.Printf(message.ChannelID, "❌ No users in your voice channel to move.")
		return nil, err
	}

	// Success in collection of Guild Channels
	return usersInVoice, nil
}

// Initialize tracking system - call this when bot starts
func initVoiceTracking() error {
	trackingMutex.Lock()
	defer trackingMutex.Unlock()

	// Try to load existing data
	if err := loadTrackedUsers(); err != nil {
		// If file doesn't exist or is corrupted, start fresh
		log.Printf("Could not load tracked users file (this is normal on first run): %v", err)
		trackedUsers = TrackedUsers{
			Guilds: make(map[string]map[string]bool),
		}
		// Save the empty structure
		return saveTrackedUsers()
	}

	log.Printf("Loaded tracked users data successfully")
	return nil
}

// Load tracked users from JSON file
func loadTrackedUsers() error {
	file, err := os.Open(jsonFilePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&trackedUsers); err != nil {
		return fmt.Errorf("failed to decode JSON: %w", err)
	}

	// Ensure the Guilds map is initialized
	if trackedUsers.Guilds == nil {
		trackedUsers.Guilds = make(map[string]map[string]bool)
	}

	return nil
}

// Save tracked users to JSON file
func saveTrackedUsers() error {
	file, err := os.Create(jsonFilePath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ") // Pretty print the JSON
	if err := encoder.Encode(&trackedUsers); err != nil {
		return fmt.Errorf("failed to encode JSON: %w", err)
	}

	return nil
}

// Check if user should be announced
func shouldAnnounceForUser(guildID, userID string) bool {
	trackingMutex.RLock()
	defer trackingMutex.RUnlock()

	// Ensure maps are initialized
	if trackedUsers.Guilds == nil || trackedUsers.Guilds[guildID] == nil {
		return false
	}

	return trackedUsers.Guilds[guildID][userID]
}

// Add user to tracking list
func addTrackedUser(guildID, userID string) error {
	trackingMutex.Lock()
	defer trackingMutex.Unlock()

	// Ensure the main Guilds map is initialized
	if trackedUsers.Guilds == nil {
		trackedUsers.Guilds = make(map[string]map[string]bool)
	}

	// Initialize guild map if it doesn't exist
	if trackedUsers.Guilds[guildID] == nil {
		trackedUsers.Guilds[guildID] = make(map[string]bool)
	}

	trackedUsers.Guilds[guildID][userID] = true

	return saveTrackedUsers()
}

// Remove user from tracking list
func removeTrackedUser(guildID, userID string) error {
	trackingMutex.Lock()
	defer trackingMutex.Unlock()

	// Ensure the main Guilds map is initialized
	if trackedUsers.Guilds == nil {
		trackedUsers.Guilds = make(map[string]map[string]bool)
	}

	if trackedUsers.Guilds[guildID] != nil {
		delete(trackedUsers.Guilds[guildID], userID)

		// Clean up empty guild maps
		if len(trackedUsers.Guilds[guildID]) == 0 {
			delete(trackedUsers.Guilds, guildID)
		}
	}

	return saveTrackedUsers()
}

// Get list of tracked users for a guild (for admin commands)
func getTrackedUsersForGuild(guildID string) []string {
	trackingMutex.RLock()
	defer trackingMutex.RUnlock()

	var users []string

	// Ensure maps are initialized
	if trackedUsers.Guilds == nil || trackedUsers.Guilds[guildID] == nil {
		return users
	}

	for userID, enabled := range trackedUsers.Guilds[guildID] {
		if enabled {
			users = append(users, userID)
		}
	}

	return users
}

// Check if user should be tracked (filter out bots, etc.)
func shouldTrackUser(s *discordgo.Session, userID string) bool {
	user, err := s.User(userID)
	if err != nil {
		return false
	}

	// Skip bots
	if user.Bot {
		return false
	}

	return true
}

// Update getAnnouncementChannel to use the config
func getAnnouncementChannel(guildID string) string {
	channelID, ok := botConfig.AnnouncementChannels[guildID]
	if !ok {
		log.Printf("No announcement channel configured for guild %s", guildID)
		return ""
	}
	return channelID
}

// Get channel name from channel ID
func getChannelName(s *discordgo.Session, channelID string) string {
	channel, err := s.Channel(channelID)
	if err != nil {
		return "Unknown Channel"
	}
	return channel.Name
}

// Get user display name (nickname or username)
func getUserDisplayName(s *discordgo.Session, guildID, userID string) string {
	member, err := s.GuildMember(guildID, userID)
	if err != nil {
		user, err := s.User(userID)
		if err != nil {
			return "Unknown User"
		}
		return user.Username
	}

	if member.Nick != "" {
		return member.Nick
	}
	return member.User.Username
}
