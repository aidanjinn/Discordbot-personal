package bot

import (
	"bufio"
	texttospeech "cloud.google.com/go/texttospeech/apiv1"
	"cloud.google.com/go/texttospeech/apiv1/texttospeechpb"
	"context"
	"encoding/binary"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"github.com/hraban/opus"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

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
	if err := waitForfileReady(actualFileName, 5*time.Second); err != nil {
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
	if err := waitForfileReady(filename, 15*time.Second); err != nil {
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
