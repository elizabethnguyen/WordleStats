package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type WordleBotAction int64

const (
	Leaderboard WordleBotAction = iota
	Post
	Help
	UpdateScore
	WipeLeaderboard
	Scoring
	Define
	Archive
	Champ
	History
	None
)

const dictionaryUri string = "https://dictionaryapi.com/api/v3/references/collegiate/json/"

var (
	token                 string
	mongoClient           *mongo.Client
	bot                   *discordgo.Session
	playerStatsCollection *mongo.Collection
	archivesCollection    *mongo.Collection
	scoreMappings         map[string]int
	dictionaryKey         string
)

type Configuration struct {
	MongoURI         string
	DiscordToken     string
	DictionaryAPIKey string
}

type PlayerStats struct {
	UserID      string              `bson:"user_id"`
	Username    string              `bson:"username"`
	GuildID     string              `bson:"guild_id"`
	TotalScore  int                 `bson:"total_score"`
	LastPuzzle  string              `bson:"last_puzzle"`
	LastUpdated primitive.Timestamp `bson:"timestamp"`
}

type Archives struct {
	ArchiveID       string              `bson:"archive_id"`
	WinningUserID   string              `bson:"winning_user_id"`
	WinningUsername string              `bson:"winning_username"`
	GuildID         string              `bson:"guild_id"`
	WinningScore    int                 `bson:"winning_score"`
	Scoreboard      string              `bson:"scoreboard"`
	Created         primitive.Timestamp `bson:"created"`
}

type DictionaryResponse struct {
	ID          string   `json:"id"`
	Definitions []string `json:"shortdef"`
}

func init() {
	flag.StringVar(&token, "t", "", "Bot Token")
	flag.Parse()

	file, _ := os.Open("wordle-conf.json")
	defer file.Close()
	decoder := json.NewDecoder(file)
	configuration := Configuration{}
	err := decoder.Decode(&configuration)
	if err != nil {
		fmt.Println("error:", err)
	}

	c, err := initMongoDB(configuration.MongoURI)
	if err != nil {
		log.Fatal("Error occurred while setting up a MongoDB connection ", err)
		os.Exit(1)
	}

	mongoClient = c

	dg, err := initDiscordBot(configuration.DiscordToken)
	if err != nil {
		log.Fatal("Error occurred while setting up the DiscordBot ", err)
		os.Exit(1)
	}

	bot = dg

	dictionaryKey = configuration.DictionaryAPIKey

	// number of turns: X, 1, 2, 3, 4, 5, 6
	scoreMappings = map[string]int{"X": 0, "1": 6, "2": 5, "3": 4, "4": 3, "5": 2, "6": 1}
}

func initMongoDB(mongoURI string) (*mongo.Client, error) {
	// spin up MongoDB connection
	clientOptions := options.Client().ApplyURI(mongoURI)
	client, err := mongo.Connect(context.TODO(), clientOptions)

	if err != nil {
		log.Fatal(err)
		return nil, err
	}

	playerStatsCollection = client.Database("WordleStats").Collection("PlayerStats")
	archivesCollection = client.Database("WordleStats").Collection("Archives")

	// Check the connection
	err = client.Ping(context.TODO(), nil)

	if err != nil {
		log.Fatal(err)
		return nil, err
	}

	return client, nil
}

func initDiscordBot(token string) (*discordgo.Session, error) {
	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatal(err)
		return nil, err
	}

	dg.AddHandler(messageCreate)
	dg.Identify.Intents = discordgo.IntentsGuildMessages

	err = dg.Open()
	if err != nil {
		log.Fatal(err)
		return nil, err
	}

	return dg, nil
}

func main() {
	fmt.Println("Bot is now running. Press CTRL+C to exit.")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc

	// close MongoDB connectiom
	err := mongoClient.Disconnect(context.TODO())

	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Connection to MongoDB closed.")

	// close bot connection
	bot.Close()

}

func messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	// ignore bot's own messages
	if m.Author.ID == s.State.User.ID {
		return
	}

	action := matchRegex(m)

	switch action {
	case Scoring:
		printScoring(m)
	case Leaderboard:
		printLeaderboard(m)
	case UpdateScore:
		adminUpdateScore(m)
	case WipeLeaderboard:
		adminWipeLeaderboard(m)
	case Post:
		handleWordlePost(m)
	case Help:
		printHelp(m)
	case Define:
		defineWord(m)
	case Archive:
		adminArchiveLeaderboard(m)
	case Champ:
		printChamp(m)
	case History:
		printHistoricalScoreboard(m)
	default:
		// do nothing
	}
}

func matchRegex(m *discordgo.MessageCreate) WordleBotAction {
	leaderboardMatch, _ := regexp.MatchString("^!leaderboard", m.Content)
	if leaderboardMatch {
		return Leaderboard
	}

	updateScoreMatch, _ := regexp.MatchString("^!updateScore", m.Content)
	if updateScoreMatch {
		return UpdateScore
	}

	wipeLeaderboardMatch, _ := regexp.MatchString("^!wipeLeaderboard$", m.Content)
	if wipeLeaderboardMatch {
		return WipeLeaderboard
	}

	scoringMatch, _ := regexp.MatchString("^!scoring$", m.Content)
	if scoringMatch {
		return Scoring
	}

	wordlePostMatch, _ := regexp.MatchString("^Wordle [0-9]+ ([0-9]|X)\\/[0-9]", m.Content)
	if wordlePostMatch {
		return Post
	}

	helpMatch, _ := regexp.MatchString("^!help$", m.Content)
	if helpMatch {
		return Help
	}

	defineMatch, _ := regexp.MatchString("^!define\\s\\w+", m.Content)
	if defineMatch {
		return Define
	}

	archiveMatch, _ := regexp.MatchString("^!archive\\s\\w+", m.Content)
	if archiveMatch {
		return Archive
	}

	champMatch, _ := regexp.MatchString("^!champ", m.Content)
	if champMatch {
		return Champ
	}

	historyMatch, _ := regexp.MatchString("^!history", m.Content)
	if historyMatch {
		return History
	}

	return None
}

func handleWordlePost(m *discordgo.MessageCreate) {
	dailyScore := regexp.MustCompile(`([0-9]|X)\/[0-9]`)
	matches := dailyScore.FindAllStringSubmatch(m.Content, 1)
	if len(matches) > 0 {
		wordleString := strings.Split(m.Content, " ")
		puzzleNumber := wordleString[1]

		// exit early if its not a valid wordle result >:(
		if !((matches[0][1] >= "1" && matches[0][1] <= "6") || matches[0][1] == "X") {
			bot.ChannelMessageSend(m.Message.ChannelID, "Invalid submission! r u tryin' 2 cheat? 👀")
			reactToScore(m, nil)
			return
		}

		stats := getPlayerStatsByAuthorAndGuild(m)
		score := 0

		if stats.UserID != "" {
			// puzzle has to be larger than the last puzzle done (no repeats)
			if puzzleNumber > stats.LastPuzzle {
				score = stats.TotalScore + scoreMappings[matches[0][1]]
				stats.LastPuzzle = puzzleNumber
			} else {
				bot.ChannelMessageSend(m.Message.ChannelID, "Invalid submission! r u tryin' 2 cheat? 👀")
				reactToScore(m, nil)
				return
			}
		} else {
			score = scoreMappings[matches[0][1]]
		}

		submitPuzzle(m, stats, score, puzzleNumber)
		reactToScore(m, matches)
	}
}

// guild specific
func getPlayerStatsByAuthorAndGuild(m *discordgo.MessageCreate) PlayerStats {
	stats := PlayerStats{}
	filter := bson.D{{Key: "user_id", Value: m.Author.ID}, {Key: "guild_id", Value: m.GuildID}}
	result := playerStatsCollection.FindOne(context.TODO(), filter, &options.FindOneOptions{})
	result.Decode(&stats)

	return stats
}

// guild specific
func getPlayerStatsByUsername(username string, guildID string) PlayerStats {
	stats := PlayerStats{}
	filter := bson.D{{Key: "username", Value: username}, {Key: "guild_id", Value: guildID}}
	result := playerStatsCollection.FindOne(context.TODO(), filter, &options.FindOneOptions{})
	result.Decode(&stats)

	return stats
}

func submitPuzzle(m *discordgo.MessageCreate, stats PlayerStats, score int, puzzleNum string) {
	filter := bson.D{{Key: "user_id", Value: m.Author.ID}, {Key: "guild_id", Value: m.GuildID}}
	opts := options.Update().SetUpsert(true)

	if stats.UserID != "" {
		stats.LastUpdated = primitive.Timestamp{T: uint32(time.Now().Unix())}
		stats.TotalScore = score
		stats.LastPuzzle = puzzleNum
	} else {
		stats = PlayerStats{m.Author.ID, m.Author.Username, m.GuildID, score, puzzleNum, primitive.Timestamp{T: uint32(time.Now().Unix())}}
	}
	updateDoc := bson.M{
		"$set": stats,
	}
	_, err := playerStatsCollection.UpdateOne(context.TODO(), filter, updateDoc, &options.UpdateOptions{}, opts)
	if err != nil {
		fmt.Println("Error while saving to db: ", err)
	}
}

// player has to already exist on the leaderboard
func updateScore(stats PlayerStats, score int) {
	filter := bson.D{{Key: "user_id", Value: stats.UserID}, {Key: "guild_id", Value: stats.GuildID}}
	opts := options.Update().SetUpsert(false)

	stats.LastUpdated = primitive.Timestamp{T: uint32(time.Now().Unix())}
	stats.TotalScore = score
	updateDoc := bson.M{
		"$set": stats,
	}
	_, err := playerStatsCollection.UpdateOne(context.TODO(), filter, updateDoc, &options.UpdateOptions{}, opts)
	if err != nil {
		fmt.Println("Error while saving to db: ", err)
	}
}

func printHelp(m *discordgo.MessageCreate) {
	msg := `**How to use WordleStats**
* **!leaderboard**: prints the current scores
* **!scoring**: prints score breakdown
* **!define [word]**: calls the Merriam-Webster API to define a word. Only picks the first definition available.
* **!updateScore [user] [value]**: _admin only_. Updates the score of an existing user on the leaderboard.
* **!wipe**: _admin only_. Wipes the leaderboard for the server.

Paste your Wordle stats into any channel in the server for WordleStats to pick it up. If WordleStats reacts to your message, your results have been recorded!

Contact skonoms#8552 for any questions, bug reports, or requests.`
	bot.ChannelMessageSend(m.Message.ChannelID, msg)
}

func getSortedLeaderboard(guildId string) []PlayerStats {
	cur, err := playerStatsCollection.Find(context.TODO(), bson.D{{Key: "guild_id", Value: guildId}})
	if err != nil {
		fmt.Println("Error while getting collection: ", err)
	}
	defer cur.Close(context.Background())
	result := []PlayerStats{}
	err = cur.All(context.TODO(), &result)
	if err != nil {
		fmt.Println("Error while decoding: ", err)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].TotalScore > result[j].TotalScore
	})

	return result
}

func generateLeaderboardString(stats []PlayerStats) string {
	resultsString := ""
	for i, r := range stats {
		scoreString := r.Username + ": " + fmt.Sprint(r.TotalScore)
		if i < len(stats)-1 {
			scoreString += "\n"
		}

		resultsString += scoreString
	}

	if resultsString == "" {
		resultsString = "No Wordles have been submitted!"
	}

	return resultsString
}

// guild specific
func printLeaderboard(m *discordgo.MessageCreate) {
	guildId := m.GuildID

	msg := strings.Split(m.Message.Content, " ") // 0 is command, 1 is guildId (optional)
	if len(msg) > 2 {
		return
	}

	if len(msg) == 2 {
		guildId = msg[1]
	}

	result := getSortedLeaderboard(guildId)
	finalString := "⭐**Wordle Leaderboard**⭐\n" + generateLeaderboardString(result)
	bot.ChannelMessageSend(m.Message.ChannelID, finalString)
}

func printScoring(m *discordgo.MessageCreate) {
	msg := `**Scoring System**
X/6: 0 points
1/6: 6 points
2/6: 5 points
3/6: 4 points
4/6: 3 points
5/6: 2 points
6/6: 1 point`
	bot.ChannelMessageSend(m.Message.ChannelID, msg)
}

func reactToScore(m *discordgo.MessageCreate, matches [][]string) {
	if matches == nil {
		bot.MessageReactionAdd(m.Message.ChannelID, m.Message.ID, "🤔")
		return
	}

	if matches[0][1] >= "1" && matches[0][1] <= "3" {
		bot.MessageReactionAdd(m.Message.ChannelID, m.Message.ID, "🔥")
	} else if matches[0][1] == "6" {
		bot.MessageReactionAdd(m.Message.ChannelID, m.Message.ID, "😰")
	} else if matches[0][1] == "X" {
		bot.MessageReactionAdd(m.Message.ChannelID, m.Message.ID, "💀")
	} else {
		bot.MessageReactionAdd(m.Message.ChannelID, m.Message.ID, "👏")
	}
}

// this is lazy and only finds the first shortdef entry
func defineWord(m *discordgo.MessageCreate) {
	resultsString := ""
	phrase := ""
	msg := strings.Split(m.Message.Content, " ") // 0 is command, 1 is word
	if len(msg) < 2 {
		return
	}

	if len(msg) == 2 {
		phrase = msg[1]
	} else {
		phrase = strings.Join(msg[1:], " ")
	}
	response, err := http.Get(dictionaryUri + phrase + "?key=" + dictionaryKey)
	if err != nil {
		return
	}

	responseData, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return
	}

	var responseObject []DictionaryResponse
	json.Unmarshal(responseData, &responseObject)

	if len(responseObject) == 0 {
		bot.ChannelMessageSend(m.Message.ChannelID, "Did not find a valid word!")
		return
	}

	if len(responseObject[0].Definitions) == 0 {
		bot.ChannelMessageSend(m.Message.ChannelID, "Did not find a valid word!")
		return
	}

	shortdefs := responseObject[0].Definitions

	for i, def := range shortdefs {
		definitionString := "* " + def
		if i < len(shortdefs)-1 {
			definitionString += "\n"
		}
		resultsString += definitionString
	}

	bot.ChannelMessageSend(m.Message.ChannelID, resultsString)
}

// guild specific
func adminWipeLeaderboard(m *discordgo.MessageCreate) {
	if m.Author.ID == "235580807098400771" { // only skonoms has permissions
		playerStatsCollection.DeleteMany(context.TODO(), bson.D{{Key: "guild_id", Value: m.GuildID}})
		bot.MessageReactionAdd(m.Message.ChannelID, m.Message.ID, "✅")
	}
}

// guild specific
func adminUpdateScore(m *discordgo.MessageCreate) {
	if m.Author.ID == "235580807098400771" { // only skonoms has permissions
		msg := strings.Split(m.Message.Content, " ") // 0 is command, 1 is name, 2 is score

		if len(msg) != 3 {
			return
		}

		username := msg[1]
		strScore := msg[2]

		score, err := strconv.Atoi(strScore)
		if err != nil {
			return
		}

		user := getPlayerStatsByUsername(username, m.GuildID)
		updateScore(user, score)
		bot.MessageReactionAdd(m.Message.ChannelID, m.Message.ID, "✅")
	}
}

// guild specific
func adminArchiveLeaderboard(m *discordgo.MessageCreate) {
	if m.Author.ID == "235580807098400771" { // only skonoms has permissions
		msg := strings.Split(m.Message.Content, " ") // 0 is command, 1 is archive name

		if len(msg) != 2 {
			return
		}

		archiveName := msg[1]

		existingArchive := getArchive(archiveName, m.GuildID)
		if existingArchive.ArchiveID != "" {
			bot.ChannelMessageSend(m.Message.ChannelID, "Archive already exists!")
			return
		}

		archiveLeaderboard(archiveName, m)
		bot.MessageReactionAdd(m.Message.ChannelID, m.Message.ID, "✅")
	}
}

// guild specific
func getArchive(a string, guildID string) Archives {
	archive := Archives{}
	filter := bson.D{{Key: "archive_id", Value: a}, {Key: "guild_id", Value: guildID}}
	result := archivesCollection.FindOne(context.TODO(), filter, &options.FindOneOptions{})
	result.Decode(&archive)

	return archive
}

// guild specific
func getLatestArchive(guildID string) Archives {
	archive := Archives{}
	filter := bson.D{{Key: "guild_id", Value: guildID}}
	opts := &options.FindOneOptions{}
	opts.SetSort(bson.D{{Key: "$natural", Value: -1}})
	result := archivesCollection.FindOne(context.TODO(), filter, opts)
	result.Decode(&archive)

	return archive
}

func archiveLeaderboard(archiveName string, m *discordgo.MessageCreate) {
	scoreboard := getSortedLeaderboard(m.GuildID)
	scoreboardString := generateLeaderboardString(scoreboard)

	a := Archives{archiveName, scoreboard[0].UserID, scoreboard[0].Username, m.GuildID, scoreboard[0].TotalScore, scoreboardString, primitive.Timestamp{T: uint32(time.Now().Unix())}}

	aDoc, err := bson.Marshal(a)
	if err != nil {
		fmt.Println("Error while marshalling: ", err)
	}

	_, err = archivesCollection.InsertOne(context.TODO(), aDoc)
	if err != nil {
		fmt.Println("Error while saving to db: ", err)
	}
}

// guild specific
func printChamp(m *discordgo.MessageCreate) {
	guildId := m.GuildID

	msg := strings.Split(m.Message.Content, " ") // 0 is command, 1 is archiveId - if not specified, will retrieve latest archive
	if len(msg) > 2 {
		return
	}

	if len(msg) == 2 {
		archiveId := msg[1]
		a := getArchive(archiveId, guildId)
		if a.ArchiveID == "" {
			bot.ChannelMessageSend(m.Message.ChannelID, "Archive not found!")
			return
		}

		finalString := fmt.Sprintf("The champion of the %s leaderboard is 👑 **%s** 👑 with a score of **%d**! 🎉", archiveId, a.WinningUsername, a.WinningScore)
		bot.ChannelMessageSend(m.Message.ChannelID, finalString)
		return
	}

	a := getLatestArchive(guildId)
	if a.ArchiveID == "" {
		bot.ChannelMessageSend(m.Message.ChannelID, "There are no archives for this server!")
		return
	}

	finalString := fmt.Sprintf("The most recent champion is 👑 **%s** 👑 with a score of **%d**! 🎉", a.WinningUsername, a.WinningScore)
	bot.ChannelMessageSend(m.Message.ChannelID, finalString)
}

// guild specific
func printHistoricalScoreboard(m *discordgo.MessageCreate) {
	guildId := m.GuildID

	msg := strings.Split(m.Message.Content, " ") // 0 is command, 1 is archiveId - if not specified, will retrieve latest archive
	if len(msg) > 2 {
		return
	}

	if len(msg) == 2 {
		archiveId := msg[1]
		a := getArchive(archiveId, guildId)
		if a.ArchiveID == "" {
			bot.ChannelMessageSend(m.Message.ChannelID, "Archive not found!")
			return
		}

		bot.ChannelMessageSend(m.Message.ChannelID, "⭐ **Archived Wordle Leaderboard - "+a.ArchiveID+"** ⭐\n"+a.Scoreboard)
		return
	}

	a := getLatestArchive(guildId)
	if a.ArchiveID == "" {
		bot.ChannelMessageSend(m.Message.ChannelID, "There are no archives for this server!")
		return
	}

	bot.ChannelMessageSend(m.Message.ChannelID, "⭐ **Archived Wordle Leaderboard - "+a.ArchiveID+"** ⭐\n"+a.Scoreboard)
}
