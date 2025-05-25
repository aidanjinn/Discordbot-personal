package bot

import (
	"context"
	"strings"
)

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
