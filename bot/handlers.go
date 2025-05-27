package bot

import (
	"context"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"
)

func sayHandler(discord *discordgo.Session, message *discordgo.MessageCreate, ttsText string) {
	go func() {
		filename := fmt.Sprintf("output_%d_%d.mp3", time.Now().Unix(), rand.Intn(10000))
		guildID := message.GuildID
		opID := fmt.Sprintf("tts_gamble_%s_%d", guildID, time.Now().Unix())
		ctx := createOperationContext(opID)
		defer removeOperationContext(opID)

		log.Printf("TTS result: %s", ttsText)
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
}

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

func joinSameChannel(discord *discordgo.Session, message *discordgo.MessageCreate) {

	guildID := message.GuildID

	go func() {

		requesterVoiceState, err := discord.State.VoiceState(guildID, message.Author.ID)

		if err != nil || requesterVoiceState == nil || requesterVoiceState.ChannelID == "" {
			discord.ChannelMessageSend(message.ChannelID, "❌ You must be in a voice channel to use this command.")
			return
		}

		targetChannelID := requesterVoiceState.ChannelID
		guild, err := discord.State.Guild(guildID)

		if err != nil {
			discord.ChannelMessageSend(message.ChannelID, "❌ Failed to get guild information.")
			return
		}

		var movedCount int
		for _, vs := range guild.VoiceStates {

			if vs.UserID == message.Author.ID || vs.ChannelID == targetChannelID {
				continue
			}

			err := discord.GuildMemberMove(guildID, vs.UserID, &targetChannelID)

			if err != nil {
				log.Printf("Failed to move user %s: %v", vs.UserID, err)
			} else {
				movedCount++
			}
		}
		discord.ChannelMessageSend(message.ChannelID, fmt.Sprintf("📢 Moved %d user(s) to your voice channel.", movedCount))
	}()

}

func randomMoveSingle(discord *discordgo.Session, message *discordgo.MessageCreate) {
	guildID := message.GuildID

	go func() {
		// Get the current voice state of the requester
		requesterVoiceState, err := discord.State.VoiceState(guildID, message.Author.ID)

		if err != nil || requesterVoiceState == nil || requesterVoiceState.ChannelID == "" {
			discord.ChannelMessageSend(message.ChannelID, "❌ You must be in a voice channel to use this command.")
			return
		}

		// Now we need to know what channel the requester is in
		targetChannelID := requesterVoiceState.ChannelID

		voiceChannels, err := gatherVoiceChannels(discord, message, guildID)
		if err != nil {
			discord.ChannelMessageSend(message.ChannelID, "Failed to get voice channels: %v")
			return
		}

		usersInVoice, err := gatherUsersVoiceStates(discord, message, guildID, targetChannelID)
		if err != nil {
			discord.ChannelMessageSend(message.ChannelID, "❌ Failure in gathering users in voice channels")
			return
		}

		selectedVoice := usersInVoice[rand.Intn(len(usersInVoice))]

		// Pick a random new channel (not the same one)
		var possibleDestinations []string
		for _, chID := range voiceChannels {
			if chID != targetChannelID {
				possibleDestinations = append(possibleDestinations, chID)
			}
		}

		if len(possibleDestinations) == 0 {
			discord.ChannelMessageSend(message.ChannelID, "❌ No other voice channels to move the user to.")
			return
		}

		newChannelID := possibleDestinations[rand.Intn(len(possibleDestinations))]

		err = discord.GuildMemberMove(guildID, selectedVoice.UserID, &newChannelID)
		if err != nil {
			log.Printf("Failed to move user: %v", err)
			discord.ChannelMessageSend(message.ChannelID, "❌ Failed to move the user.")
			return
		}

		discord.ChannelMessageSend(message.ChannelID, fmt.Sprintf("<@%s> 🔫 Has Been Shot", selectedVoice.UserID))
	}()
}

func shuffleVoiceChannels(discord *discordgo.Session, message *discordgo.MessageCreate) {
	guildID := message.GuildID

	// Run in goroutine to avoid blocking
	go func() {

		voiceChannels, err := gatherVoiceChannels(discord, message, guildID)
		if err != nil {
			discord.ChannelMessageSend(message.ChannelID, "Failed to get voice channels: %v")
		}
		usersInVoice, err := gatherUsersVoiceStates(discord, message, guildID, "")
		if err != nil {
			discord.ChannelMessageSend(message.ChannelID, "Failed to get users voice states: %v")
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
		sayHandler(discord, message, ttsText)
	}()
}

func handleImageMessage(discord *discordgo.Session, message *discordgo.MessageCreate) {
	attachment := message.Attachments[0]
	imageURL := attachment.URL
	filename := attachment.Filename
	prompt := strings.TrimSpace(strings.Replace(message.Content, "!see", "", 1))

	imagePath, err := downloadAttachment(imageURL, filename)
	if err != nil {
		discord.ChannelMessageSend(message.ChannelID, "Failed to download image.")
		return
	}
	defer os.Remove(imagePath) // cleanup

	ctx := context.Background()
	response, err := imageProcess(ctx, imagePath, prompt)
	if err != nil {
		discord.ChannelMessageSend(message.ChannelID, "Gemini image processing failed: "+err.Error())
		return
	}

	// Ensure response fits in Discord message
	if len(response) > 2000 {
		response = response[:1997] + "..."
	}

	discord.ChannelMessageSend(message.ChannelID, response)
	sayHandler(discord, message, response)
}

func askHandler(discord *discordgo.Session, message *discordgo.MessageCreate, guildID string) {
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

		if len(reply) > 2000 {
			for len(reply) > 2000 {
				discord.ChannelMessageSend(message.ChannelID, reply[:2000])
				reply = reply[2000:]
			}
		}
		discord.ChannelMessageSend(message.ChannelID, reply)
		fmt.Sprintf("gemini_output_%d_%d.mp3", time.Now().Unix(), rand.Intn(10000))
		sayHandler(discord, message, reply)
	}()
}

func imageGenerationHandler(discord *discordgo.Session, message *discordgo.MessageCreate, prompt string, guildID string) {
	go func() {
		discord.ChannelMessageSend(message.ChannelID, "🎨 Generating image for prompt: *"+prompt+"*...")

		ctx := context.Background()
		var imagePath string

		// Check for image attachments
		if len(message.Attachments) > 0 {

			attachment := message.Attachments[0]

			if strings.HasPrefix(attachment.ContentType, "image/") {

				// Download image to a temp file
				resp, err := http.Get(attachment.URL)
				if err != nil {
					discord.ChannelMessageSend(message.ChannelID, "❌ Failed to download attached image.")
					return
				}
				defer resp.Body.Close()

				tempFile, err := os.CreateTemp("", "discord_image_*.png")
				if err != nil {
					discord.ChannelMessageSend(message.ChannelID, "❌ Failed to create temporary image file.")
					return
				}
				defer tempFile.Close()

				_, err = io.Copy(tempFile, resp.Body)
				if err != nil {
					discord.ChannelMessageSend(message.ChannelID, "❌ Failed to save attached image.")
					return
				}

				imagePath = tempFile.Name()
				defer os.Remove(imagePath) // clean up local file
			}
		}

		// Call image generator
		filename, err := generateImageFromPrompt(ctx, prompt, imagePath)
		if err != nil {
			discord.ChannelMessageSend(message.ChannelID, "❌ Failed to generate image: "+err.Error())
			return
		}

		file, err := os.Open(filename)
		if err != nil {
			discord.ChannelMessageSend(message.ChannelID, "❌ Failed to open generated image.")
			return
		}
		defer file.Close()

		discord.ChannelFileSend(message.ChannelID, filename, file)

		// Clean up generated image after 30s
		time.AfterFunc(30*time.Second, func() {
			removeTempFile(guildID, filename)
		})
	}()
}

func handleUserJoinedVoice(s *discordgo.Session, vsu *discordgo.VoiceStateUpdate, after *discordgo.VoiceState, userName string) {
	channelName := getChannelName(s, after.ChannelID)

	// Join voice channel
	vc, err := s.ChannelVoiceJoin(vsu.GuildID, after.ChannelID, false, false)
	
	// Create new voice session with context
	ctx, cancel := context.WithCancel(context.Background())
	session := &VoiceSession{
		connection: vc,
		ctx:        ctx,
		cancel:     cancel,
		isPlaying:  false,
	}

	botManager.mu.Lock()
	botManager.voiceConnections[vsu.GuildID] = session
	botManager.mu.Unlock()

	if err != nil {
		log.Printf("Error joining voice channel: %v", err)
		return
	}

	// Create or get existing session for this guild
	session, ok := sessions[vsu.GuildID]
	if !ok {
		ctx, cancel := context.WithCancel(context.Background())
		session = &VoiceSession{
			connection: vc,
			ctx:        ctx,
			cancel:     cancel,
		}
		sessions[vsu.GuildID] = session
	} else {
		// If a session exists, replace connection if necessary
		session.connection = vc
	}

	// Generate TTS text
	ttsText := "Welcome to " + channelName + " " + userName

	go func() {
		filename := fmt.Sprintf("output_%d_%d.mp3", time.Now().Unix(), rand.Intn(10000))
		opID := fmt.Sprintf("tts_gamble_%s_%d", vsu.GuildID, time.Now().Unix())
		ctx2 := createOperationContext(opID)
		defer removeOperationContext(opID)

		log.Printf("TTS result: %s", ttsText)
		err := synthesizeToMP3(ctx2, ttsText, filename)
		if err != nil {
			log.Printf("❌ TTS failed: %v", err)
			return
		}

		// Play audio with your function
		playMP3(session, filename, s, getAnnouncementChannel(vsu.GuildID))

		// Optional: clean up file after some time
		time.AfterFunc(30*time.Second, func() {
			os.Remove(filename)
		})
	}()

}

func onVoiceStateUpdate(s *discordgo.Session, vsu *discordgo.VoiceStateUpdate) {

	log.Printf("Voice state update: %v", vsu)

	// Skip if user shouldn't be tracked
	if !shouldTrackUser(s, vsu.UserID) {
		log.Printf("Should Not Track User: %v", vsu)
		return
	}

	// Skip if not configured to announce for this specific user
	if !shouldAnnounceForUser(vsu.GuildID, vsu.UserID) {
		log.Printf("Should Not Annouce User: %v", vsu)
		return
	}

	announceChannelID := getAnnouncementChannel(vsu.GuildID)

	if announceChannelID == "" {
		log.Print("No ChannelID given to announce:")
		return
	}

	before := vsu.BeforeUpdate
	after := vsu.VoiceState

	userName := getUserDisplayName(s, vsu.GuildID, vsu.UserID)
	switch {
	//JOIN CHANNEL
	case before == nil && after.ChannelID != "":
		handleUserJoinedVoice(s, vsu, after, userName)

	//SWITCH CHANNEL
	case before != nil && before.ChannelID != after.ChannelID && after.ChannelID != "":
		handleUserJoinedVoice(s, vsu, after, userName)
	}
}

func handleTrackingCommands(discord *discordgo.Session, message *discordgo.MessageCreate) {
	guildID := message.GuildID
	userID := message.Author.ID

	switch {
	case strings.Contains(message.Content, "!track me"):
		if err := addTrackedUser(guildID, userID); err != nil {
			discord.ChannelMessageSend(message.ChannelID, "❌ Failed to enable voice tracking: "+err.Error())
			return
		}
		discord.ChannelMessageSend(message.ChannelID, "✅ You will now be announced when joining/leaving voice channels!")

	case strings.Contains(message.Content, "!untrack me"):
		if err := removeTrackedUser(guildID, userID); err != nil {
			discord.ChannelMessageSend(message.ChannelID, "❌ Failed to disable voice tracking: "+err.Error())
			return
		}
		discord.ChannelMessageSend(message.ChannelID, "❌ Voice announcements disabled for you.")

	case strings.Contains(message.Content, "!tracked list"):
		// Admin command to see who's being tracked
		users := getTrackedUsersForGuild(guildID)
		if len(users) == 0 {
			discord.ChannelMessageSend(message.ChannelID, "📋 No users are currently being tracked for voice announcements.")
			return
		}

		var userList string
		for _, uid := range users {
			userName := getUserDisplayName(discord, guildID, uid)
			userList += fmt.Sprintf("• %s (`%s`)\n", userName, uid)
		}

		response := fmt.Sprintf("📋 **Tracked Users (%d):**\n%s", len(users), userList)
		if len(response) > 2000 {
			response = response[:1997] + "..."
		}
		discord.ChannelMessageSend(message.ChannelID, response)
	}
}

func newMessage(discord *discordgo.Session, message *discordgo.MessageCreate) {
	if message.Author == nil || message.Author.ID == discord.State.User.ID {
		return
	}

	guildID := message.GuildID

	switch {
	case strings.Contains(message.Content, "!help"):
		commandList := "**🎮 Wang Bot Command List:**\n" +
			"```" +
			"💡 !help        → Show this command list\n" +
			"💦 !cum         → Play a cursed custom sound\n" +
			"🎵 !play        → Play an audio file (e.g., Heyooo.mp3, Lorenzofuckingdies.mp3)\n" +
			"📺 !ytplay      → Play audio from a YouTube link\n" +
			"🔌 !connect     → Connect the bot to a voice channel\n" +
			"❌ !disconnect  → Disconnect the bot from the voice channel\n" +
			"🧠 !ask         → Ask Gemini AI (supports text + Image Attachments)\n" +
			"🗣️ !say         → Make the bot speak using text-to-speech\n" +
			"🔀 !shuffle     → Shuffle users in voice channels randomly\n" +
			"🎰 !gamble      → Spin the slot machine (big risk, big reward)\n" +
			"📞 !recall      → Summon the whole squad to voice\n" +
			"🛑 !kill        → Stop all current bot actions\n" +
			"🔫 !shoot        → Wang Bot Shoots a Random User\n" +
			"🎨 !create      → Ask Wang Bot To Create an Image (Image Attachments Supported)\n" +
			"   !track me\n" +
			"   !untrack me\n" +
			"   !track list\n" +
			"```"
		discord.ChannelMessageSend(message.ChannelID, "Command List:\n"+commandList)

	case strings.Contains(message.Content, "!kill"):
		go func() {
			killGuildOperations(guildID)
			discord.ChannelMessageSend(message.ChannelID, "🛑 Killed all active operations for this server.")

		}()

	case strings.Contains(message.Content, "!shuffle"):
		go func() {
			shuffleVoiceChannels(discord, message)
		}()

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
			trimmed := strings.TrimPrefix(message.Content, "!say ")
			sayHandler(discord, message, trimmed)
		}()

	case strings.HasPrefix(message.Content, "!see ") && len(message.Attachments) > 0:
		go func() {
			handleImageMessage(discord, message)
		}()

	case strings.HasPrefix(message.Content, "!ask "):

		// If !ask has an image case
		if len(message.Attachments) > 0 {
			go func() {
				handleImageMessage(discord, message)
			}()
		} else {
			go func() {
				askHandler(discord, message, guildID)
			}()
		}

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

	case strings.Contains(message.Content, "!recall"):
		go func() {
			joinSameChannel(discord, message)
		}()

	case strings.Contains(message.Content, "!shoot"):
		go func() {
			randomMoveSingle(discord, message)
		}()

	case strings.HasPrefix(message.Content, "!create "):
		go func() {
			trimmed := strings.TrimPrefix(message.Content, "!create ")
			imageGenerationHandler(discord, message, trimmed, guildID)
		}()

	case strings.Contains(message.Content, "!track me"):
		handleTrackingCommands(discord, message)
	case strings.Contains(message.Content, "!untrack me"):
		handleTrackingCommands(discord, message)
	case strings.Contains(message.Content, "!tracked list"):
		handleTrackingCommands(discord, message)

	}
}
