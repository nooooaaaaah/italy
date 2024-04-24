package main

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
)

func getDiscordToken() string {
	err := godotenv.Load()
	if err != nil {
		fmt.Println("Error loading .env file")
		return ""
	}
	return os.Getenv("DISCORD_BOT_TOKEN")
}

// Token should be your Bot's Token. Remember to keep it safe and not expose it in public repositories.

var (
	Token          = getDiscordToken()
	VotePercentage = 75.0                          // The percentage of votes needed to take action.
	voteSessions   = make(map[string]*VoteSession) // Tracks ongoing vote sessions by channel.
	sessionLock    sync.Mutex
)

// VoteSession represents a voting session within a Discord channel.
type VoteSession struct {
	GuildID      string
	ChannelID    string
	VotesNeeded  int
	Votes        int
	UserToKick   string
	Voters       map[string]bool // Tracks who has voted to prevent duplicate votes.
	VoteComplete chan bool       // Signals when the vote is complete.
}

// voiceStates keeps track of the voice state of users to manage voice-related commands.
var voiceStates = make(map[string]*discordgo.VoiceState)

func main() {
	// Create a new Discord session using the provided bot token.
	if Token == "" {
		fmt.Println("error reading token from .env")
		return
	}
	dg, err := discordgo.New("Bot " + Token)
	if err != nil {
		fmt.Println("error creating Discord session,", err)
		return
	}

	// Register the voiceStateUpdate function as a handler for VoiceStateUpdate events.
	dg.AddHandler(voiceStateUpdate)

	// Open a websocket connection to Discord and begin listening.
	err = dg.Open()
	if err != nil {
		fmt.Println("error opening connection,", err)
		return
	}
	// Send a message to my guild on startup
	go sendMessageToGuild(dg, "769609920000688188")

	// Define commands that the bot can accept.
	commands := []*discordgo.ApplicationCommand{
		{
			Name:        "italy",
			Description: "Starts a vote to boot a user from the voice channel.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "The user to vote to kick",
					Required:    true,
				},
			},
		},
		{
			Name:        "vote",
			Description: "Participate in a vote to boot a user",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionBoolean,
					Name:        "vote",
					Description: "Enter true or false to kick a user",
					Required:    true,
				},
			},
		},
	}

	// Map command names to handler functions.
	commandHandlers := map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate){
		"italy": startVote,
		"vote":  addVote,
	}

	// Register commands with Discord.
	for _, cmd := range commands {
		_, err := dg.ApplicationCommandCreate(dg.State.User.ID, "", cmd)
		if err != nil {
			fmt.Printf("Cannot create '%v' command: %v\n", cmd.Name, err)
		}
	}

	// Add a handler for processing commands.
	dg.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if h, ok := commandHandlers[i.ApplicationCommandData().Name]; ok {
			h(s, i)
		}
	})

	fmt.Println("Bot is now running. Press CTRL-C to exit.")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt, os.Kill)
	<-sc
	dg.Close()
}

func sendMessageToGuild(s *discordgo.Session, guildID string) {
	channels, err := s.GuildChannels(guildID)
	if err != nil {
		fmt.Printf("Failed to fetch channels for guild %s: %v\n", guildID, err)
		return
	}

	for _, channel := range channels {
		if channel.Type == discordgo.ChannelTypeGuildText {
			// Check if the bot has permissions to send messages in this channel
			perms, err := s.State.UserChannelPermissions(s.State.User.ID, channel.ID)
			if err == nil && perms&discordgo.PermissionSendMessages != 0 {
				_, err := s.ChannelMessageSend(channel.ID, "Bot is now running and ready to receive commands!")
				if err != nil {
					fmt.Printf("Failed to send message to channel %s in guild %s: %v\n", channel.ID, guildID, err)
				} else {
					fmt.Printf("Startup message sent to channel %s in guild %s\n", channel.ID, guildID)
					break // Stop after successfully sending to one channel per guild
				}
			}
		}
	}
}

// voiceStateUpdate updates the voice state of users as they join, leave, or move between voice channels.
func voiceStateUpdate(s *discordgo.Session, vsu *discordgo.VoiceStateUpdate) {
	voiceStates[vsu.UserID] = vsu.VoiceState
	fmt.Printf("Updated voice state for user %s\n", vsu.UserID)
}

// countMembersInChannel counts the number of members in a given voice channel.
func countMembersInChannel(channelID string) int {
	count := 0
	for _, vs := range voiceStates {
		if vs.ChannelID == channelID {
			count++
		}
	}
	fmt.Printf("Counted %d members in channel %s\n", count, channelID)
	return count
}

// startVote initializes a vote to kick a user from a voice channel.
func startVote(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// Check if the initiator is in a voice channel
	initiator := i.Member.User.ID
	initiatorVoiceState, ok := voiceStates[initiator]
	if !ok || initiatorVoiceState.ChannelID == "" {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "You need to be in a voice channel to start a vote.",
			},
		})
		fmt.Println("Vote initiation failed: Initiator not in voice channel")
		return
	}

	// Validate the target user
	targetUser := i.ApplicationCommandData().Options[0].UserValue(s)
	if targetUser == nil {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Invalid user specified.",
			},
		})
		fmt.Println("Vote initiation failed: Invalid target user")
		return
	}

	// Check if the target is in the same voice channel as the initiator
	targetVoiceState, ok := voiceStates[targetUser.ID]
	if !ok || targetVoiceState.ChannelID != initiatorVoiceState.ChannelID {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Both you and the target must be in the same voice channel.",
			},
		})
		fmt.Println("Vote initiation failed: Target not in same voice channel")
		return
	}
	guildID := initiatorVoiceState.GuildID
	// Calculate the required votes and start the voting session
	currentChannel := initiatorVoiceState.ChannelID
	totalMembers := countMembersInChannel(currentChannel)
	requiredVotes := int(float64(totalMembers) * VotePercentage / 100)

	session := &VoteSession{
		GuildID:      guildID,
		ChannelID:    currentChannel,
		VotesNeeded:  requiredVotes,
		UserToKick:   targetUser.ID,
		Voters:       make(map[string]bool),
		VoteComplete: make(chan bool),
	}
	voteSessions[currentChannel] = session

	go handleVoteCompletion(session, s)

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("Vote to kick %s started. Type /vote to participate. This vote will conclude in 60 seconds.", targetUser.Username),
		},
	})
	fmt.Printf("Vote to kick %s initiated in channel %s\n", targetUser.Username, currentChannel)
}

func addVote(s *discordgo.Session, i *discordgo.InteractionCreate) {
	voterID := i.Member.User.ID
	voterVoiceState, ok := voiceStates[voterID]
	if !ok || voterVoiceState.ChannelID == "" {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "You need to be in a voice channel to vote.",
			},
		})
		fmt.Println("Vote attempt failed: Voter not in voice channel")
		return
	}

	sessionLock.Lock()
	session, ok := voteSessions[voterVoiceState.ChannelID]
	sessionLock.Unlock()
	if !ok {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "There is no active vote in this voice channel.",
			},
		})
		fmt.Println("Vote attempt failed: No active vote session in voice channel")
		return
	}

	sessionLock.Lock()
	if _, voted := session.Voters[voterID]; voted {
		sessionLock.Unlock()
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "You have already voted.",
			},
		})
		fmt.Println("Vote attempt failed: Voter has already voted")
		return
	}

	voteValue := i.ApplicationCommandData().Options[0].BoolValue()
	if voteValue {
		session.Votes++
	}
	session.Voters[voterID] = true
	sessionLock.Unlock()

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "Your vote has been counted.",
		},
	})
	go sendMessageForCurrentVoteSession(session, s)
	fmt.Printf("Vote recorded: %s voted %t\n", voterID, voteValue)
}

func sendMessageForCurrentVoteSession(session *VoteSession, s *discordgo.Session) {
	message := fmt.Sprintf("Current votes: %d/%d required to kick the user.", session.Votes, session.VotesNeeded)
	s.ChannelMessageSend(session.ChannelID, message)
	fmt.Printf("Vote status message sent in channel %s. %d votes out of %d needed.\n", session.ChannelID, session.Votes, session.VotesNeeded)
}

func handleVoteCompletion(session *VoteSession, s *discordgo.Session) {
	time.Sleep(60 * time.Second) // Simulate vote duration
	sessionLock.Lock()
	defer sessionLock.Unlock()

	if session.Votes >= session.VotesNeeded {
		msg, err := s.ChannelMessageSend(session.ChannelID, "Vote passed. User will be kicked.")
		if err != nil {
			fmt.Println("Error sending vote passed message: ", err)
		}

		timeoutDuration := time.Now().Add(300 * time.Second)
		if err := s.GuildMemberTimeout(session.GuildID, session.UserToKick, &timeoutDuration); err != nil {
			fmt.Println("Error applying timeout to user: ", err)
		} else {
			fmt.Printf("User %s timed out for 60 seconds in guild %s.\n", session.UserToKick, session.GuildID)
		}
		fmt.Printf("message should be sent to channel %v with msg ID %v", session.ChannelID, msg.ID)
		// Add your kicking logic here
		fmt.Printf("Vote complete: Vote passed, user %s will be kicked from channel %s.\n", session.UserToKick, session.ChannelID)
	} else {
		s.ChannelMessageSend(session.ChannelID, "Vote failed. Not enough votes.")
		fmt.Printf("Vote complete: Vote failed, not enough votes to kick user %s from channel %s.\n", session.UserToKick, session.ChannelID)
	}

	delete(voteSessions, session.ChannelID) // Clean up the session after completion
}
