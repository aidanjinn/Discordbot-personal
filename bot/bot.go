package bot

import (
	"context"
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
