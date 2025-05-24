package bot

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"github.com/hraban/opus"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"strings"
)

/*
Global Vars: (later shift some of these inside a bot struct to call)

	BotToken -> Pulled From .ENV File used to for Discord Connection
	voiceConnections -> Current Channel Connections (For use in voice channel connections)
	encode -> used for the mp3 Encoder/Piper for audio file playing
*/
var BotToken string
var voiceConnections = make(map[string]*discordgo.VoiceConnection) // guildID -> VoiceConnection
var encoder *opus.Encoder

// Standard Golang Error Handling
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

	discord.Close()
}

/*
Method Check for if Bot is current within the same voice channel as requester (User)

	If so True Else False
	Used in determining if a connect needs to occur
*/
func botIsInSameVoiceChannel(discord *discordgo.Session, guildID, userID string) bool {
	// Get user's voice state
	userVoiceState, err := discord.State.VoiceState(guildID, userID)

	// If any of these conditions trigger the user not in any voice channel
	if userVoiceState == nil || userVoiceState.ChannelID == "" || err != nil {
		return false
	}

	// Then we see if bot is connected in this guild
	vc, ok := voiceConnections[guildID]
	if !ok || vc == nil {
		return false
	}

	// Finally we compare the bot's channel with the user's channel (to see if both are in the same voice channel)
	return vc.ChannelID == userVoiceState.ChannelID
}

/*
Method for init. a bot connection the requesters voice channel

	Checks:
		1) We see if the bot is already within the same channel as the user
			-> note this check also includes checks for if the user is within a channel, or the bot is in the server already
		2) Seeing if the Requester is joined the guild/server
		3) Seeing if the Requester if within a voice channel
		Once we passed the these checks then we init. a connection (the bot to join the voice channel the user is within)
*/
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
	checkNilErr(err)

	if voiceState == nil || voiceState.ChannelID == "" {
		discord.ChannelMessageSend(message.ChannelID, "User is not connected to a voice channel.")
		return false
	}

	vc, err := discord.ChannelVoiceJoin(guildID, voiceState.ChannelID, false, false)
	checkNilErr(err)

	voiceConnections[guildID] = vc
	discord.ChannelMessageSend(message.ChannelID, "✅ Connected to Voice Channel")

	customMsg := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			Content:   "!play Heyooo.mp3",
			ChannelID: message.ChannelID,
			GuildID:   message.GuildID,
		},
	}
	soundPlay(discord, customMsg)

	return true
}

func soundPlay(discord *discordgo.Session, message *discordgo.MessageCreate) {

	mp3 := strings.TrimSpace(strings.TrimPrefix(message.Content, "!play "))
	guildID := message.GuildID
	vc, ok := voiceConnections[guildID]

	if !ok || vc == nil {
		discord.ChannelMessageSend(message.ChannelID, "❌ Bot is not connected to a voice channel.")
		return
	}

	go playMP3(vc, mp3, discord, message.ChannelID)
}

func shuffleVoiceChannels(discord *discordgo.Session, message *discordgo.MessageCreate) {
	guildID := message.GuildID

	// Fetch all channels and filter out voice channels
	channels, err := discord.GuildChannels(guildID)
	checkNilErr(err)

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

	// Fetch guild voice states (users in voice channels)
	guild, err := discord.State.Guild(guildID)
	checkNilErr(err)

	var usersInVoice []*discordgo.VoiceState
	for _, vs := range guild.VoiceStates {
		usersInVoice = append(usersInVoice, vs)
	}

	if len(usersInVoice) < 1 {
		discord.ChannelMessageSend(message.ChannelID, "❌ No users in voice channels to shuffle.")
		return
	}

	// Shuffle users
	rand.Shuffle(len(usersInVoice), func(i, j int) {
		usersInVoice[i], usersInVoice[j] = usersInVoice[j], usersInVoice[i]
	})

	// Assign each user to a voice channel, cycling through them
	for i, vs := range usersInVoice {
		targetChannel := voiceChannels[i%len(voiceChannels)]
		err := discord.GuildMemberMove(guildID, vs.UserID, &targetChannel)
		if err != nil {
			log.Printf("Failed to move user %s: %v", vs.UserID, err)
		}
	}

	discord.ChannelMessageSend(message.ChannelID, "🔀 Shuffled users into random voice channels.")
}

/*
Message Handler Containing the case logic for supported commands

	!help -> command list
	!cum -> this is just a custom sound play
	!play -> play 'filename.mp3'
	!connect
	!disconnect
*/
func newMessage(discord *discordgo.Session, message *discordgo.MessageCreate) {

	if message.Author == nil || message.Author.ID == discord.State.User.ID {
		return
	}

	switch {

	case strings.Contains(message.Content, "!help"):
		commandList := "\t!help -> command list\n\t!cum -> this is just a custom sound play\n\t!play -> play 'filename.mp3' : Heyooo.mp3 and Lorenzofuckingdies.mp3\n\t!connect\n\t!disconnect"
		discord.ChannelMessageSend(message.ChannelID, "Command List:\n"+commandList)

	case strings.Contains(message.Content, "!shuffle 1024"):
		shuffleVoiceChannels(discord, message)

	case strings.Contains(message.Content, "!cum"):

		botConnect(discord, message)

		customMsg := &discordgo.MessageCreate{
			Message: &discordgo.Message{
				Content:   "!play Lorenzofuckingdies.mp3",
				ChannelID: message.ChannelID,
				GuildID:   message.GuildID,
			},
		}
		soundPlay(discord, customMsg)

	case strings.HasPrefix(message.Content, "!play "):
		botConnect(discord, message)
		soundPlay(discord, message)

	case strings.Contains(message.Content, "!connect"):
		botConnect(discord, message)

	case strings.Contains(message.Content, "!disconnect"):
		guildID := message.GuildID
		if vc, ok := voiceConnections[guildID]; ok && vc != nil {
			err := vc.Disconnect()
			checkNilErr(err)
			delete(voiceConnections, guildID)
			discord.ChannelMessageSend(message.ChannelID, "Good Bye 👋")
		} else {
			discord.ChannelMessageSend(message.ChannelID, "I'm not connected to a voice channel in this guild.")
		}
	}
}

func playMP3(vc *discordgo.VoiceConnection, filename string, discord *discordgo.Session, channelID string) {
	vc.Speaking(true)
	defer vc.Speaking(false)

	cmd := exec.Command("ffmpeg", "-i", filename, "-f", "s16le", "-ar", "48000", "-ac", "2", "pipe:1")
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
		buf := make([]int16, 960*2) // 20ms of stereo
		err := binary.Read(reader, binary.LittleEndian, &buf)
		if err != nil {
			break
		}
		vc.OpusSend <- pcmToOpus(buf)
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
