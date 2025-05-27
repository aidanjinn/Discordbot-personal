package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"github.com/hraban/opus"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
)

var BotToken string
var voiceConnections = make(map[string]*discordgo.VoiceConnection)
var encoder *opus.Encoder

var tempFiles = make(map[string][]string) // guildID -> list of temp files
var tempFilesMu sync.RWMutex

var activeOperations = make(map[string]*OperationContext)
var operationsMu sync.RWMutex

var configFilePath = "config.json"
var sessions = make(map[string]*VoiceSession) // key: guildID
var sessionsMu sync.Mutex

// TrackedUsers represents the structure of our JSON file
type TrackedUsers struct {
	Guilds map[string]map[string]bool `json:"guilds"` // guildID -> userID -> enabled
}

type BotConfig struct {
	TrackedUsers         TrackedUsers      `json:"tracked_users"`
	AnnouncementChannels map[string]string `json:"announcement_channels"` // guildID -> channelID
}

// Global variables to hold tracked users data
var (
	trackedUsers  TrackedUsers
	trackingMutex sync.RWMutex
	jsonFilePath  = "tracked_users.json"
	botConfig     BotConfig // Assuming this is defined elsewhere
)

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

type OperationContext struct {
	ctx    context.Context
	cancel context.CancelFunc
}

var botManager = &BotManager{
	voiceConnections: make(map[string]*VoiceSession),
}

func checkNilErr(e error) {
	if e != nil {
		log.Fatal(e)
	}
}

func killGuildOperations(guildID string) {
	operationsMu.Lock()
	defer operationsMu.Unlock()

	// Kill any active operations for this guild!
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

		// make sure to remove this connection
		delete(botManager.voiceConnections, guildID)
	}

	// Clean up temp files for this guild
	cleanupTempFiles(guildID)
}

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

func removeOperationContext(operationID string) {
	operationsMu.Lock()
	defer operationsMu.Unlock()

	if op, exists := activeOperations[operationID]; exists {
		op.cancel()
		delete(activeOperations, operationID)
	}
}

func loadBotConfig() error {
	file, err := os.Open(configFilePath)
	if err != nil {
		return fmt.Errorf("failed to open config file: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&botConfig); err != nil {
		return fmt.Errorf("failed to decode config JSON: %w", err)
	}

	// Make sure maps are initialized
	if botConfig.TrackedUsers.Guilds == nil {
		botConfig.TrackedUsers.Guilds = make(map[string]map[string]bool)
	}
	if botConfig.AnnouncementChannels == nil {
		botConfig.AnnouncementChannels = make(map[string]string)
	}

	return nil
}

func Run() {

	if err := loadBotConfig(); err != nil {
		log.Fatalf("Failed to load bot config: %v", err)
	}

	discord, err := discordgo.New("Bot " + BotToken)
	checkNilErr(err)

	// Initialize voice tracking
	if err := initVoiceTracking(); err != nil {
		log.Fatalf("Failed to initialize voice tracking: %v", err)
	}

	// Register the voice state update handler - ADD THIS LINE
	discord.AddHandler(onVoiceStateUpdate)
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
