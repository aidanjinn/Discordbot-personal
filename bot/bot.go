package bot

import (
	"bufio"
	texttospeech "cloud.google.com/go/texttospeech/apiv1"
	"context"
	"encoding/binary"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"github.com/hraban/opus"
	"google.golang.org/genai"
	texttospeechpb "google.golang.org/genproto/googleapis/cloud/texttospeech/v1"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var BotToken string
var voiceConnections = make(map[string]*discordgo.VoiceConnection)
var encoder *opus.Encoder

// Add this near the top with other global variables
var tempFiles = make(map[string][]string) // guildID -> list of temp files
var tempFilesMu sync.RWMutex

// Modified killGuildOperations function
func killGuildOperations(guildID string) {
	operationsMu.Lock()
	defer operationsMu.Unlock()

	// Kill any active operations for this guild
	for opID, op := range activeOperations {
		if strings.Contains(opID, guildID) {
			op.cancel()
			delete(activeOperations, opID)
		}
	}

	// Stop voice playback
	botManager.mu.Lock()
	defer botManager.mu.Unlock()

	if session, exists := botManager.voiceConnections[guildID]; exists {
		session.cancel()
		session.mu.Lock()
		session.isPlaying = false
		session.mu.Unlock()
	}

	// Clean up temp files for this guild
	cleanupTempFiles(guildID)
}

// Modified killAllOperations function
func killAllOperations() {
	operationsMu.Lock()
	defer operationsMu.Unlock()

	for opID, op := range activeOperations {
		op.cancel()
		delete(activeOperations, opID)
	}

	botManager.mu.Lock()
	defer botManager.mu.Unlock()

	for guildID, session := range botManager.voiceConnections {
		session.cancel()
		if session.connection != nil {
			session.connection.Disconnect()
		}
		delete(botManager.voiceConnections, guildID)
	}

	// Clean up all temp files
	cleanupAllTempFiles()
}

// Add these new functions for temp file management
func addTempFile(guildID, filename string) {
	tempFilesMu.Lock()
	defer tempFilesMu.Unlock()

	if tempFiles[guildID] == nil {
		tempFiles[guildID] = make([]string, 0)
	}
	tempFiles[guildID] = append(tempFiles[guildID], filename)
}

func removeTempFile(guildID, filename string) {
	tempFilesMu.Lock()
	defer tempFilesMu.Unlock()

	if files, exists := tempFiles[guildID]; exists {
		for i, file := range files {
			if file == filename {
				tempFiles[guildID] = append(files[:i], files[i+1:]...)
				break
			}
		}
		// Clean up empty slice
		if len(tempFiles[guildID]) == 0 {
			delete(tempFiles, guildID)
		}
	}

	// Remove the actual file
	os.Remove(filename)
}

func cleanupTempFiles(guildID string) {
	tempFilesMu.Lock()
	defer tempFilesMu.Unlock()

	if files, exists := tempFiles[guildID]; exists {
		for _, filename := range files {
			os.Remove(filename)
			log.Printf("Cleaned up temp file: %s", filename)
		}
		delete(tempFiles, guildID)
	}
}

func cleanupAllTempFiles() {
	tempFilesMu.Lock()
	defer tempFilesMu.Unlock()

	for _, files := range tempFiles {
		for _, filename := range files {
			os.Remove(filename)
			log.Printf("Cleaned up temp file: %s", filename)
		}
	}
	tempFiles = make(map[string][]string)
}

// Helper function to wait for file to be ready
func waitForFile(filename string, maxWait time.Duration) error {
	start := time.Now()
	for time.Since(start) < maxWait {
		if info, err := os.Stat(filename); err == nil && info.Size() > 0 {
			// File exists and has content, wait a bit more to ensure it's fully written
			time.Sleep(100 * time.Millisecond)
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("file %s not ready after %v", filename, maxWait)
}

// Modified downloadAndPlayYT function
func downloadAndPlayYT(ctx context.Context, discord *discordgo.Session, channelID, guildID, url string) {
	select {
	case <-ctx.Done():
		discord.ChannelMessageSend(channelID, "❌ YouTube download cancelled.")
		return
	default:
	}

	botManager.mu.RLock()
	session, ok := botManager.voiceConnections[guildID]
	botManager.mu.RUnlock()

	if !ok || session == nil || session.connection == nil {
		discord.ChannelMessageSend(channelID, "❌ Not connected to a voice channel.")
		return
	}

	// Generate a temp file name (without extension for yt-dlp template)
	baseFileName := fmt.Sprintf("yt_audio_%d_%d", time.Now().Unix(), rand.Intn(100000))
	outputTemplate := baseFileName + ".%(ext)s"

	// Create command with context for cancellation
	cmd := exec.CommandContext(ctx, "yt-dlp", "-x", "--audio-format", "mp3", "-o", outputTemplate, url)

	discord.ChannelMessageSend(channelID, "⬇️ Downloading YouTube audio...")

	// Capture command output for debugging
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			discord.ChannelMessageSend(channelID, "❌ YouTube download cancelled.")
		} else {
			discord.ChannelMessageSend(channelID, "❌ Failed to download audio.")
			log.Printf("yt-dlp error: %v\nOutput: %s", err, string(output))
		}
		return
	}

	// The actual filename will have .mp3 extension
	actualFileName := baseFileName + ".mp3"

	// Check if file exists
	if _, err := os.Stat(actualFileName); os.IsNotExist(err) {
		// Try alternative extensions that yt-dlp might use
		alternatives := []string{".webm", ".m4a", ".opus", ".ogg"}
		found := false
		for _, ext := range alternatives {
			testName := baseFileName + ext
			if _, err := os.Stat(testName); err == nil {
				// Convert to mp3 using ffmpeg
				convertCmd := exec.CommandContext(ctx, "ffmpeg", "-i", testName, "-acodec", "mp3", actualFileName)
				convertErr := convertCmd.Run()
				if convertErr == nil {
					os.Remove(testName) // Remove original file
					found = true
					break
				} else {
					log.Printf("Failed to convert %s to mp3: %v", testName, convertErr)
				}
			}
		}

		if !found {
			discord.ChannelMessageSend(channelID, "❌ Downloaded file not found or conversion failed.")
			log.Printf("Expected file %s not found", actualFileName)
			return
		}
	}

	// Wait for file to be ready
	if err := waitForFile(actualFileName, 5*time.Second); err != nil {
		discord.ChannelMessageSend(channelID, "❌ File not ready: "+err.Error())
		return
	}

	// Add temp file to tracking
	addTempFile(guildID, actualFileName)

	discord.ChannelMessageSend(channelID, "🎵 Playing downloaded YouTube audio...")
	playMP3(session, actualFileName, discord, channelID)

	// Cleanup after a delay to ensure playback completes, but also remove from tracking
	time.AfterFunc(5*time.Minute, func() {
		removeTempFile(guildID, actualFileName)
	})
}

// Global context and cancellation for managing bot operations
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

// Context for long-running operations
type OperationContext struct {
	ctx    context.Context
	cancel context.CancelFunc
}

var activeOperations = make(map[string]*OperationContext)
var operationsMu sync.RWMutex

func checkNilErr(e error) {
	if e != nil {
		log.Fatal(e)
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

// Create a new operation context
func createOperationContext(operationID string) context.Context {
	operationsMu.Lock()
	defer operationsMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	activeOperations[operationID] = &OperationContext{
		ctx:    ctx,
		cancel: cancel,
	}
	return ctx
}

// Remove operation context
func removeOperationContext(operationID string) {
	operationsMu.Lock()
	defer operationsMu.Unlock()

	if op, exists := activeOperations[operationID]; exists {
		op.cancel()
		delete(activeOperations, operationID)
	}
}

// Fixed Gemini response function using the official Google AI Go SDK
func getGeminiResponse(ctx context.Context, prompt string) (string, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("GEMINI_API_KEY is not set")
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})

	if err != nil {
		return "", fmt.Errorf("failed to create Gemini client: %w", err)
	}

	promptIntro := "You are a crazy schizophrenia, conspiracy believing, paranoid old chinese man named Wang, and you speak with a fuckton curse words\n"
	promptTheories := "Here is a list of theories : {alien's bring bigfoot to earth 10000 years ago to protect them from dinosaurs while the mined gold on earth, elites buying children to harvest adrenochrome, MKUltra," +
		" The government poisoning the water to turn the youth and frogs gay, Agartha, anunnaki, and babylonians}" +
		" : END OF EXAMPLES " +
		": BRAIN AND THOUGHT PROCESSES : {In your response DO NOT just use one or all the examples given; Take those examples, using your LLM database of information (on google) and come " +
		"  up and respond with different crazy ideas I want your response to have a proper conclusion and if asked a QUESTION given AN ANSWER to it}\n"
	promptEnd := "Return a crazy response to this statement prompt:{" + prompt + "} with a statement you would say : (only the response : make sure your RESPONSE IS UNDER 4000 characters)\n"
	totalPrompt := promptIntro + promptTheories + promptEnd

	result, err := client.Models.GenerateContent(
		ctx,
		"gemini-2.0-flash",
		genai.Text(totalPrompt),
		nil,
	)
	if err != nil {
		return "", fmt.Errorf("failed to generate content: %w", err)
	}

	response := result.Text()

	if response == "" {
		return "", fmt.Errorf("empty response from Gemini")
	}

	return response, nil
}

// Modified synthesizeToMP3 with better error handling and file verification
func synthesizeToMP3(ctx context.Context, text string, filename string) error {
	err := os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "tts-cred.json")
	if err != nil {
		return fmt.Errorf("failed to set credentials env: %w", err)
	}

	client, err := texttospeech.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("texttospeech.NewClient: %w", err)
	}
	defer client.Close()

	req := &texttospeechpb.SynthesizeSpeechRequest{
		Input: &texttospeechpb.SynthesisInput{
			InputSource: &texttospeechpb.SynthesisInput_Text{Text: strings.ToLower(text)},
		},
		Voice: &texttospeechpb.VoiceSelectionParams{
			LanguageCode: "cmn-CN",
			Name:         "cmn-CN-Chirp3-HD-Achird",
			SsmlGender:   texttospeechpb.SsmlVoiceGender_MALE,
		},
		AudioConfig: &texttospeechpb.AudioConfig{
			AudioEncoding: texttospeechpb.AudioEncoding_MP3,
		},
	}

	resp, err := client.SynthesizeSpeech(ctx, req)
	if err != nil {
		return fmt.Errorf("SynthesizeSpeech: %w", err)
	}

	// Ensure directory exists
	dir := filepath.Dir(filename)
	if dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory: %w", err)
		}
	}

	err = ioutil.WriteFile(filename, resp.AudioContent, 0644)
	if err != nil {
		return fmt.Errorf("ioutil.WriteFile: %w", err)
	}

	// Verify file was written correctly
	if info, err := os.Stat(filename); err != nil {
		return fmt.Errorf("failed to verify file: %w", err)
	} else if info.Size() == 0 {
		return fmt.Errorf("generated file is empty")
	}

	log.Printf("Successfully created TTS file: %s (size: %d bytes)", filename)
	return nil
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

func soundPlay(discord *discordgo.Session, message *discordgo.MessageCreate) {
	mp3 := strings.TrimSpace(strings.TrimPrefix(message.Content, "!play "))
	guildID := message.GuildID

	botManager.mu.RLock()
	session, ok := botManager.voiceConnections[guildID]
	botManager.mu.RUnlock()

	if !ok || session == nil || session.connection == nil {
		discord.ChannelMessageSend(message.ChannelID, "❌ Bot is not connected to a voice channel.")
		return
	}

	// Play in goroutine to avoid blocking
	go playMP3(session, mp3, discord, message.ChannelID)
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

// Fixed message handler with better synchronous processing for file operations
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
			if err := waitForFile(filename, 10*time.Second); err != nil {
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
			if err := waitForFile(filename, 10*time.Second); err != nil {
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
	}
}

func pcmToOpus(pcm []int16) []byte {
	if encoder == nil {
		var err error
		encoder, err = opus.NewEncoder(48000, 2, opus.AppAudio)
		checkNilErr(err)
	}

	opusBuf := make([]byte, 1000)
	n, err := encoder.Encode(pcm, opusBuf)
	checkNilErr(err)
	return opusBuf[:n]
}

func playMP3(session *VoiceSession, filename string, discord *discordgo.Session, channelID string) {
	session.mu.Lock()
	if session.isPlaying {
		session.mu.Unlock()
		return // Already playing something
	}
	session.isPlaying = true
	session.mu.Unlock()

	defer func() {
		session.mu.Lock()
		session.isPlaying = false
		session.mu.Unlock()
	}()

	vc := session.connection
	if vc == nil {
		return
	}

	// Wait for file to be fully ready before attempting to play
	if err := waitForFileReady(filename, 15*time.Second); err != nil {
		discord.ChannelMessageSend(channelID, "❌ Audio file not ready: "+err.Error())
		log.Printf("File not ready for playback: %v", err)
		return
	}

	// Additional verification - try to open and check file integrity
	if err := verifyAudioFile(filename); err != nil {
		discord.ChannelMessageSend(channelID, "❌ Audio file verification failed: "+err.Error())
		log.Printf("File verification failed: %v", err)
		return
	}

	vc.Speaking(true)
	defer vc.Speaking(false)

	// Create command with context for cancellation
	cmd := exec.CommandContext(session.ctx, "ffmpeg", "-i", filename, "-f", "s16le", "-ar", "48000", "-ac", "2", "pipe:1")
	stdout, err := cmd.StdoutPipe()

	if err != nil {
		discord.ChannelMessageSend(channelID, "❌ Failed to stream audio.")
		log.Println("Failed to create ffmpeg pipe:", err)
		return
	}

	if err := cmd.Start(); err != nil {
		discord.ChannelMessageSend(channelID, "❌ ffmpeg failed to start.")
		log.Println("ffmpeg start failed:", err)
		return
	}

	reader := bufio.NewReaderSize(stdout, 16384)
	for {
		// Check if context is cancelled
		select {
		case <-session.ctx.Done():
			cmd.Process.Kill()
			return
		default:
		}

		buf := make([]int16, 960*2)
		err := binary.Read(reader, binary.LittleEndian, &buf)
		if err != nil {
			break
		}

		select {
		case vc.OpusSend <- pcmToOpus(buf):
		case <-session.ctx.Done():
			cmd.Process.Kill()
			return
		}
	}

	cmd.Wait()
}

// Enhanced file readiness check with better validation
func waitForFileReady(filename string, maxWait time.Duration) error {
	start := time.Now()
	var lastSize int64 = -1
	stableCount := 0

	for time.Since(start) < maxWait {
		info, err := os.Stat(filename)
		if err != nil {
			if os.IsNotExist(err) {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			return fmt.Errorf("file stat error: %w", err)
		}

		currentSize := info.Size()

		// File must have content
		if currentSize == 0 {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		// Check if file size is stable (not still being written to)
		if currentSize == lastSize {
			stableCount++
			// File size has been stable for at least 500ms
			if stableCount >= 5 {
				// Additional check - try to open file to ensure it's not locked
				file, err := os.OpenFile(filename, os.O_RDONLY, 0)
				if err != nil {
					time.Sleep(100 * time.Millisecond)
					continue
				}
				file.Close()

				log.Printf("File %s is ready (size: %d bytes, stable for %dms)",
					filename, currentSize, stableCount*100)
				return nil
			}
		} else {
			stableCount = 0
			lastSize = currentSize
		}

		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("file %s not ready after %v (last size: %d)", filename, maxWait, lastSize)
}

// Verify audio file integrity before attempting to play
func verifyAudioFile(filename string) error {
	// Quick ffprobe check to verify file integrity
	cmd := exec.Command("ffprobe", "-v", "error", "-select_streams", "a:0",
		"-show_entries", "stream=duration", "-of", "csv=p=0", filename)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffprobe verification failed: %w (output: %s)", err, string(output))
	}

	// If ffprobe can read the file and get duration, it's likely valid
	outputStr := strings.TrimSpace(string(output))
	if outputStr == "" || outputStr == "N/A" {
		return fmt.Errorf("file appears to be invalid or corrupted")
	}

	log.Printf("Audio file verified successfully (duration: %s)", outputStr)
	return nil
}
