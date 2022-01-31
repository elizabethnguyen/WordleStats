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
	Define
	Archive
	Champ
	History
	SetDifficulty
	SetScoring
	GetSettings
	ResetSettings
	AddPermissions
	RemovePermissions
	GetDistribution
	None
)

const (
	ANY  string = "ANY"
	HARD string = "HARD"
	EASY string = "EASY"
)

const dictionaryUri string = "https://dictionaryapi.com/api/v3/references/collegiate/json/"
const defaultGuildSettings string = `Difficulty: ANY
1/6: 6
2/6: 5
3/6: 4
4/6: 3
5/6: 2
6/6: 1
X/6: 0`

const defaultDifficulty string = ANY

var defaultScores []int

var (
	token                          string
	mongoClient                    *mongo.Client
	bot                            *discordgo.Session
	playerStatsCollection          *mongo.Collection
	archivesCollection             *mongo.Collection
	guildSettingsCollection        *mongo.Collection
	permissionsCollection          *mongo.Collection
	distributionArchivesCollection *mongo.Collection
	defaultScoreMappings           map[string]int
	dictionaryKey                  string
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
	TotalOne    int                 `bson:"total_one"`
	TotalTwo    int                 `bson:"total_two"`
	TotalThree  int                 `bson:"total_three"`
	TotalFour   int                 `bson:"total_four"`
	TotalFive   int                 `bson:"total_five"`
	TotalSix    int                 `bson:"total_six"`
	TotalX      int                 `bson:"total_x"`
	LastUpdated primitive.Timestamp `bson:"timestamp"`
}

type Archives struct {
	ArchiveID       string              `bson:"archive_id"`
	WinningUserID   string              `bson:"winning_user_id"`
	WinningUsername string              `bson:"winning_username"`
	GuildID         string              `bson:"guild_id"`
	WinningScore    int                 `bson:"winning_score"`
	Scoreboard      string              `bson:"scoreboard"`
	Difficulty      string              `bson:"difficulty"`
	Created         primitive.Timestamp `bson:"created"`
}

type PlayerDistributionArchive struct {
	UserID     string              `bson:"user_id"`
	Username   string              `bson:"username"`
	GuildID    string              `bson:"guild_id"`
	ArchiveID  string              `bson:"archive_id"`
	TotalScore int                 `bson:"total_score"`
	TotalOne   int                 `bson:"total_one"`
	TotalTwo   int                 `bson:"total_two"`
	TotalThree int                 `bson:"total_three"`
	TotalFour  int                 `bson:"total_four"`
	TotalFive  int                 `bson:"total_five"`
	TotalSix   int                 `bson:"total_six"`
	TotalX     int                 `bson:"total_x"`
	Created    primitive.Timestamp `bson:"created"`
}

type GuildSettings struct {
	GuildID     string              `bson:"guild_id"`
	Difficulty  string              `bson:"difficulty"`
	ScoreOne    int                 `bson:"score_one"`
	ScoreTwo    int                 `bson:"score_two"`
	ScoreThree  int                 `bson:"score_three"`
	ScoreFour   int                 `bson:"score_four"`
	ScoreFive   int                 `bson:"score_five"`
	ScoreSix    int                 `bson:"score_six"`
	ScoreX      int                 `bson:"score_x"`
	LastUpdated primitive.Timestamp `bson:"last_updated"`
}

type Permissions struct {
	UserID  string `bson:"user_id"`
	GuildID string `bson:"guild_id"`
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
	defaultScoreMappings = map[string]int{"X": 0, "1": 6, "2": 5, "3": 4, "4": 3, "5": 2, "6": 1}
	defaultScores = []int{6, 5, 4, 3, 2, 1, 0}
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
	guildSettingsCollection = client.Database("WordleStats").Collection("GuildSettings")
	permissionsCollection = client.Database("WordleStats").Collection("Permissions")
	distributionArchivesCollection = client.Database("WordleStats").Collection("DistributionArchives")

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
	case SetDifficulty:
		adminSetServerDifficulty(m)
	case SetScoring:
		adminSetServerScoring(m)
	case GetSettings:
		printGuildSettings(m)
	case ResetSettings:
		adminResetGuildSettings(m)
	case AddPermissions:
		adminAddPermissions(m)
	case RemovePermissions:
		adminRemovePermissions(m)
	case GetDistribution:
		getDistribution(m)
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

	setDifficultyMatch, _ := regexp.MatchString("^!setDifficulty", m.Content)
	if setDifficultyMatch {
		return SetDifficulty
	}

	setScoringMatch, _ := regexp.MatchString("^!setScoring -?[0-9]+ -?[0-9]+ -?[0-9]+ -?[0-9]+ -?[0-9]+ -?[0-9]+ -?[0-9]+$", m.Content)
	if setScoringMatch {
		return SetScoring
	}

	getSettingsMatch, _ := regexp.MatchString("^!getSettings", m.Content)
	if getSettingsMatch {
		return GetSettings
	}

	resetSettingsMatch, _ := regexp.MatchString("^!resetSettings$", m.Content)
	if resetSettingsMatch {
		return ResetSettings
	}

	addPermissionsMatch, _ := regexp.MatchString("^!addPermissions ", m.Content)
	if addPermissionsMatch {
		return AddPermissions
	}

	removePermissionsMatch, _ := regexp.MatchString("^!removePermissions ", m.Content)
	if removePermissionsMatch {
		return RemovePermissions
	}

	getDistributionMatch, _ := regexp.MatchString("^!getDistribution ", m.Content)
	if getDistributionMatch {
		return GetDistribution
	}

	return None
}

func handleWordlePost(m *discordgo.MessageCreate) {
	var matches [][]string
	difficulty := ANY
	scoreMappings := defaultScoreMappings
	scoreMatchEasy := `([0-9]|X)\/[0-9]`
	scoreMatchHard := `([0-9]|X)\/[0-9]\*`

	settings := getGuildSettings(m.GuildID)
	if settings.GuildID != "" {
		difficulty = settings.Difficulty
		scoreMappings = map[string]int{"X": settings.ScoreX, "1": settings.ScoreOne, "2": settings.ScoreTwo, "3": settings.ScoreThree, "4": settings.ScoreFour, "5": settings.ScoreFive, "6": settings.ScoreSix}
	}

	easyScore := regexp.MustCompile(scoreMatchEasy)
	hardScore := regexp.MustCompile(scoreMatchHard)
	easyMatches := easyScore.FindAllStringSubmatch(m.Content, 1)
	hardMatches := hardScore.FindAllStringSubmatch(m.Content, 1)

	// regex is being funky so hardmode submissions will have matches on both
	if len(easyMatches) > 0 && len(hardMatches) == 0 {
		if difficulty == HARD {
			bot.ChannelMessageSend(m.Message.ChannelID, "Invalid submission! Server settings are set for hard mode only.")
			return
		}
		matches = easyMatches
	} else if len(hardMatches) > 0 {
		if difficulty == EASY {
			bot.ChannelMessageSend(m.Message.ChannelID, "Invalid submission! Server settings are set for easy mode only.")
			return
		}
		matches = hardMatches
	} else {
		return
	}

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

	submitPuzzle(m, stats, score, puzzleNumber, matches[0][1])
	reactToScore(m, matches)
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

// guild specific
func getGuildSettings(guildID string) GuildSettings {
	settings := GuildSettings{}
	filter := bson.D{{Key: "guild_id", Value: guildID}}
	result := guildSettingsCollection.FindOne(context.TODO(), filter, &options.FindOneOptions{})
	result.Decode(&settings)

	return settings
}

func submitPuzzle(m *discordgo.MessageCreate, stats PlayerStats, score int, puzzleNum string, numAttempts string) {
	filter := bson.D{{Key: "user_id", Value: m.Author.ID}, {Key: "guild_id", Value: m.GuildID}}
	opts := options.Update().SetUpsert(true)

	if stats.UserID != "" {
		stats.LastUpdated = primitive.Timestamp{T: uint32(time.Now().Unix())}
		stats.TotalScore = score
		stats.LastPuzzle = puzzleNum
		switch numAttempts {
		case "1":
			stats.TotalOne += 1
		case "2":
			stats.TotalTwo += 1
		case "3":
			stats.TotalThree += 1
		case "4":
			stats.TotalFour += 1
		case "5":
			stats.TotalFive += 1
		case "6":
			stats.TotalSix += 1
		case "X":
			stats.TotalX += 1
		default:
			// do nothing
		}
	} else {
		stats = PlayerStats{m.Author.ID,
			m.Author.Username,
			m.GuildID, score,
			puzzleNum,
			0, 0, 0, 0, 0, 0, 0, // score distributions
			primitive.Timestamp{T: uint32(time.Now().Unix())},
		}
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
* **!define [word]**: calls the Merriam-Webster API to define a word. Only picks the first definition available.
* **!history [archiveName]**: look at a leaderboard snapshot. If no name is specified, will retrieve the most recent archive.
* **!champ [archiveName]**: outputs the top score + player for a specific snapshot. If none is specified, will retrieve the top player from the most recent archive.
* **!getSettings**: outputs the difficulty rule and scoring system for the server.
* **!getDistribution [username] [archiveName]**: archive name optional. Outputs the scoring distribution for current or archived leaderboard.
* **!help**: prints this message.

**Admin-Only**
* **!updateScore [user] [value]**: updates the score of an existing user on the leaderboard.
* **!wipeLeaderboard**: wipes the leaderboard for the server.
* **!archive [archiveName]**: saves a snapshot of the current leaderboard to a named archive.
* **!resetSettings**: resets the difficulty and scoriing system to default.
* **!setDifficulty [ANY/HARD/EASY]**: rule enforcement for the type of submissions that WordleStats will recognize.
* **!setScoring [1] [2] [3] [4] [5] [6] [X]**: sets scoring system; must have all fields specified (negative values accepted)

**skonoms-Only**
* **!addPermissions [userId] [guildId]**: gives specified user admin permissions for the specified server
* **!removePermissions [userId] [guildId]**: removes specified user admin permissions for the specified server

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

func getSingleDistributionStringByUsernameAndGuild(username string, guildId string, archiveName string) string {
	finalString := ""
	if archiveName == "" { // get current
		stats := PlayerStats{}
		filter := bson.D{{Key: "username", Value: username}, {Key: "guild_id", Value: guildId}}
		result := playerStatsCollection.FindOne(context.TODO(), filter, &options.FindOneOptions{})
		result.Decode(&stats)
		if stats.UserID != "" {
			finalString = convertToDistributionStringFromStats(stats)
		}
	} else {
		dist := PlayerDistributionArchive{}
		filter := bson.D{{Key: "username", Value: username}, {Key: "guild_id", Value: guildId}, {Key: "archive_id", Value: archiveName}}
		result := distributionArchivesCollection.FindOne(context.TODO(), filter, &options.FindOneOptions{})
		result.Decode(&dist)
		if dist.UserID != "" {
			finalString = convertToDistributionString(dist)
		}
	}

	return finalString
}

func convertToDistributionStringFromStats(stats PlayerStats) string {
	return fmt.Sprintf(`⭐**Distribution stats for current leaderboard**⭐
	Score: %d
	1/6: %d
	2/6: %d
	3/6: %d
	4/6: %d
	5/6: %d
	6/6: %d
	X/6: %d`, stats.TotalScore, stats.TotalOne, stats.TotalTwo, stats.TotalThree, stats.TotalFour, stats.TotalFive, stats.TotalSix, stats.TotalX)
}

func convertToDistributionString(dist PlayerDistributionArchive) string {
	return fmt.Sprintf(`⭐**Distribution stats for leaderboard %s**⭐
	Score: %d
	1/6: %d
	2/6: %d
	3/6: %d
	4/6: %d
	5/6: %d
	6/6: %d
	X/6: %d`, dist.ArchiveID, dist.TotalScore, dist.TotalOne, dist.TotalTwo, dist.TotalThree, dist.TotalFour, dist.TotalFive, dist.TotalSix, dist.TotalX)
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

func generateDistributions(stats []PlayerStats, archive string) []PlayerDistributionArchive {
	var distributions []PlayerDistributionArchive
	for _, r := range stats {
		d := PlayerDistributionArchive{
			r.UserID,
			r.Username,
			r.GuildID,
			archive,
			r.TotalScore,
			r.TotalOne,
			r.TotalTwo,
			r.TotalThree,
			r.TotalFour,
			r.TotalFive,
			r.TotalSix,
			r.TotalX,
			primitive.Timestamp{T: uint32(time.Now().Unix())},
		}

		distributions = append(distributions, d)
	}

	return distributions
}

// guild specific
func printLeaderboard(m *discordgo.MessageCreate) {
	difficulty := ANY
	guildId := m.GuildID

	settings := getGuildSettings(guildId)
	if settings.GuildID != "" {
		difficulty = settings.Difficulty
	}

	msg := strings.Split(m.Message.Content, " ") // 0 is command, 1 is guildId (optional)
	if len(msg) > 2 {
		return
	}

	if len(msg) == 2 {
		guildId = msg[1]
	}

	result := getSortedLeaderboard(guildId)

	headerString := "⭐**Current Wordle Leaderboard - difficulty: " + difficulty + "**⭐\n"
	finalString := headerString + generateLeaderboardString(result)
	bot.ChannelMessageSend(m.Message.ChannelID, finalString)
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
	difficulty := ANY
	scoreboard := getSortedLeaderboard(m.GuildID)
	scoreboardString := generateLeaderboardString(scoreboard)
	distributions := generateDistributions(scoreboard, archiveName)

	settings := getGuildSettings(m.GuildID)

	if settings.GuildID != "" {
		difficulty = settings.Difficulty
	}

	a := Archives{archiveName, scoreboard[0].UserID, scoreboard[0].Username, m.GuildID, scoreboard[0].TotalScore, scoreboardString, difficulty, primitive.Timestamp{T: uint32(time.Now().Unix())}}
	saveArchive(a)
	saveDistributions(distributions)
}

func saveArchive(a Archives) {
	aDoc, err := bson.Marshal(a)
	if err != nil {
		fmt.Println("Error while marshalling: ", err)
	}

	_, err = archivesCollection.InsertOne(context.TODO(), aDoc)
	if err != nil {
		fmt.Println("Error while saving to db: ", err)
	}
}

func saveDistributions(distributions []PlayerDistributionArchive) {
	var iface []interface{}
	for _, d := range distributions {
		iface = append(iface, d)
	}

	_, err := distributionArchivesCollection.InsertMany(context.TODO(), iface)
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

// guild specific, only supports usernames
func getDistribution(m *discordgo.MessageCreate) {
	msg := strings.Split(m.Message.Content, " ") // 0 is command, 1 is username, 2 is archiveId - if not specified, will retrieve current leaderboard
	if len(msg) > 2 {
		return
	}

	guildId := m.GuildID
	username := msg[1]
	archiveId := ""

	if len(msg) == 3 {
		archiveId = msg[2]
	}

	finalString := getSingleDistributionStringByUsernameAndGuild(username, guildId, archiveId)

	if finalString == "" {
		bot.ChannelMessageSend(m.Message.ChannelID, "Could not find any distribution data!")
	} else {
		bot.ChannelMessageSend(m.Message.ChannelID, finalString)
	}
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

		headerString := "⭐ **Archived Wordle Leaderboard - " + a.ArchiveID + ", difficulty: " + a.Difficulty + "** ⭐\n"
		bot.ChannelMessageSend(m.Message.ChannelID, headerString+a.Scoreboard)
		return
	}

	a := getLatestArchive(guildId)
	if a.ArchiveID == "" {
		bot.ChannelMessageSend(m.Message.ChannelID, "There are no archives for this server!")
		return
	}

	headerString := "⭐ **Archived Wordle Leaderboard - " + a.ArchiveID + ", difficulty: " + a.Difficulty + "** ⭐\n"
	bot.ChannelMessageSend(m.Message.ChannelID, headerString+a.Scoreboard)
}

func setServerDifficulty(guildId string, difficulty string) {
	settings := GuildSettings{
		GuildID:     guildId,
		Difficulty:  difficulty,
		ScoreOne:    defaultScoreMappings["1"],
		ScoreTwo:    defaultScoreMappings["2"],
		ScoreThree:  defaultScoreMappings["3"],
		ScoreFour:   defaultScoreMappings["4"],
		ScoreFive:   defaultScoreMappings["5"],
		ScoreSix:    defaultScoreMappings["6"],
		ScoreX:      defaultScoreMappings["X"],
		LastUpdated: primitive.Timestamp{T: uint32(time.Now().Unix())}}

	existingSettings := getGuildSettings(guildId)

	if existingSettings.GuildID != "" {
		settings.ScoreOne = existingSettings.ScoreOne
		settings.ScoreTwo = existingSettings.ScoreTwo
		settings.ScoreThree = existingSettings.ScoreThree
		settings.ScoreFour = existingSettings.ScoreFour
		settings.ScoreFive = existingSettings.ScoreFive
		settings.ScoreSix = existingSettings.ScoreSix
		settings.ScoreX = existingSettings.ScoreX
	}

	filter := bson.D{{Key: "guild_id", Value: guildId}}
	opts := options.Update().SetUpsert(true)

	updateDoc := bson.M{
		"$set": settings,
	}
	_, err := guildSettingsCollection.UpdateOne(context.TODO(), filter, updateDoc, &options.UpdateOptions{}, opts)
	if err != nil {
		fmt.Println("Error while saving to db: ", err)
	}
}

// guild specific
func adminSetServerScoring(m *discordgo.MessageCreate) {
	perms := getPermissionsForUserAndGuild(m.Author.ID, m.GuildID)

	if perms.UserID != "" || m.Author.ID == "235580807098400771" { // only skonoms has permissions
		guildId := m.GuildID

		msg := strings.Split(m.Message.Content, " ") // 0 is command, 1-8 are scores
		if len(msg) != 8 {
			return
		}

		strSlice := msg[1:]
		var scoring = []int{}

		for _, i := range strSlice {
			j, err := strconv.Atoi(i)
			if err != nil {
				return
			}
			scoring = append(scoring, j)
		}

		setServerScoring(guildId, scoring)
		bot.MessageReactionAdd(m.Message.ChannelID, m.Message.ID, "✅")
	} else {
		bot.MessageReactionAdd(m.Message.ChannelID, m.Message.ID, "❌")
	}
}

func setServerScoring(guildId string, scoring []int) {
	settings := GuildSettings{
		GuildID:     guildId,
		Difficulty:  ANY,
		ScoreOne:    scoring[0],
		ScoreTwo:    scoring[1],
		ScoreThree:  scoring[2],
		ScoreFour:   scoring[3],
		ScoreFive:   scoring[4],
		ScoreSix:    scoring[5],
		ScoreX:      scoring[6],
		LastUpdated: primitive.Timestamp{T: uint32(time.Now().Unix())}}

	existingSettings := getGuildSettings(guildId)

	if existingSettings.GuildID != "" {
		settings.Difficulty = existingSettings.Difficulty
	}

	filter := bson.D{{Key: "guild_id", Value: guildId}}
	opts := options.Update().SetUpsert(true)

	updateDoc := bson.M{
		"$set": settings,
	}
	_, err := guildSettingsCollection.UpdateOne(context.TODO(), filter, updateDoc, &options.UpdateOptions{}, opts)
	if err != nil {
		fmt.Println("Error while saving to db: ", err)
	}
}

// guild specific
func printGuildSettings(m *discordgo.MessageCreate) {
	guildId := m.GuildID
	msg := strings.Split(m.Message.Content, " ") // 0 is command, 1 is guildId - if not specified, prints for current guild
	if len(msg) > 2 {
		return
	}

	if len(msg) == 2 {
		guildId = msg[1]
	}

	settings := getGuildSettings(guildId)
	if settings.GuildID == "" {
		bot.ChannelMessageSend(m.Message.ChannelID, defaultGuildSettings)
		return
	}

	bot.ChannelMessageSend(m.Message.ChannelID, formatGuildSettingsMessage(settings))
}

func formatGuildSettingsMessage(settings GuildSettings) string {
	return fmt.Sprintf(`Difficulty: %s
1/6: %d
2/6: %d
3/6: %d
4/6: %d
5/6: %d
6/6: %d
X/6: %d`,
		settings.Difficulty, settings.ScoreOne, settings.ScoreTwo,
		settings.ScoreThree, settings.ScoreFour, settings.ScoreFive,
		settings.ScoreSix, settings.ScoreX)
}

func addPermissions(userId string, guildId string) {
	perms := Permissions{UserID: userId, GuildID: guildId}
	filter := bson.D{{Key: "user_id", Value: userId}, {Key: "guild_id", Value: guildId}}
	opts := options.Update().SetUpsert(true)

	updateDoc := bson.M{
		"$set": perms,
	}
	_, err := permissionsCollection.UpdateOne(context.TODO(), filter, updateDoc, &options.UpdateOptions{}, opts)
	if err != nil {
		fmt.Println("Error while saving to db: ", err)
	}
}

func removePermissions(userId string, guildId string) {
	filter := bson.D{{Key: "user_id", Value: userId}, {Key: "guild_id", Value: guildId}}
	opts := options.Delete()

	_, err := permissionsCollection.DeleteOne(context.TODO(), filter, &options.DeleteOptions{}, opts)
	if err != nil {
		fmt.Println("Error while deleting from db: ", err)
	}
}

// guild specific
func getPermissionsForUserAndGuild(userId string, guildId string) Permissions {
	perms := Permissions{}
	filter := bson.D{{Key: "user_id", Value: userId}, {Key: "guild_id", Value: guildId}}
	result := permissionsCollection.FindOne(context.TODO(), filter, &options.FindOneOptions{})
	result.Decode(&perms)

	return perms
}

// guild specific, only skonoms has permission
func adminRemovePermissions(m *discordgo.MessageCreate) {
	if m.Author.ID == "235580807098400771" { // only skonoms has permissions
		msg := strings.Split(m.Message.Content, " ") // 0 is command, 1 is userId, 2 is guildId
		if len(msg) != 3 {
			return
		}

		userId := msg[1]
		guildId := msg[2]

		removePermissions(userId, guildId)
		bot.MessageReactionAdd(m.Message.ChannelID, m.Message.ID, "✅")
	}
}

// guild specific, multiple admins allowed
func adminArchiveLeaderboard(m *discordgo.MessageCreate) {
	perms := getPermissionsForUserAndGuild(m.Author.ID, m.GuildID)

	if perms.UserID != "" || m.Author.ID == "235580807098400771" { // skonoms always has permissions
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
	} else {
		bot.MessageReactionAdd(m.Message.ChannelID, m.Message.ID, "❌")
	}
}

// guild specific, multiple admins allowed
func adminResetGuildSettings(m *discordgo.MessageCreate) {
	perms := getPermissionsForUserAndGuild(m.Author.ID, m.GuildID)

	if perms.UserID != "" || m.Author.ID == "235580807098400771" { // skonoms always has permissions
		guildId := m.GuildID

		setServerScoring(guildId, defaultScores)
		setServerDifficulty(guildId, defaultDifficulty)
		bot.MessageReactionAdd(m.Message.ChannelID, m.Message.ID, "✅")
	} else {
		bot.MessageReactionAdd(m.Message.ChannelID, m.Message.ID, "❌")
	}
}

// guild specific, only skonoms has permission
func adminAddPermissions(m *discordgo.MessageCreate) {
	if m.Author.ID == "235580807098400771" { // only skonoms has permissions
		msg := strings.Split(m.Message.Content, " ") // 0 is command, 1 is userId, 2 is guildId
		if len(msg) != 3 {
			return
		}

		userId := msg[1]
		guildId := msg[2]

		addPermissions(userId, guildId)
		bot.MessageReactionAdd(m.Message.ChannelID, m.Message.ID, "✅")
	}
}

// guild specific, multiple admins allowed
func adminWipeLeaderboard(m *discordgo.MessageCreate) {
	perms := getPermissionsForUserAndGuild(m.Author.ID, m.GuildID)

	if perms.UserID != "" || m.Author.ID == "235580807098400771" { // skonoms always has permissions
		playerStatsCollection.DeleteMany(context.TODO(), bson.D{{Key: "guild_id", Value: m.GuildID}})
		bot.MessageReactionAdd(m.Message.ChannelID, m.Message.ID, "✅")
	} else {
		bot.MessageReactionAdd(m.Message.ChannelID, m.Message.ID, "❌")
	}
}

// guild specific, multiple admins allowed
func adminUpdateScore(m *discordgo.MessageCreate) {
	perms := getPermissionsForUserAndGuild(m.Author.ID, m.GuildID)

	if perms.UserID != "" || m.Author.ID == "235580807098400771" { // skonoms always has permissions
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
	} else {
		bot.MessageReactionAdd(m.Message.ChannelID, m.Message.ID, "❌")
	}
}

// guild specific, multiple admins allowed
func adminSetServerDifficulty(m *discordgo.MessageCreate) {
	perms := getPermissionsForUserAndGuild(m.Author.ID, m.GuildID)

	if perms.UserID != "" || m.Author.ID == "235580807098400771" { // skonoms always has permissions
		guildId := m.GuildID

		msg := strings.Split(m.Message.Content, " ") // 0 is command, 1 is difficulty - valid values are ANY, EASY, HARD
		if len(msg) > 2 {
			return
		}

		difficulty := msg[1]

		if difficulty != ANY && difficulty != EASY && difficulty != HARD {
			bot.ChannelMessageSend(m.Message.ChannelID, "Invalid difficulty! Supported values: [ANY, EASY, HARD]")
			return
		}

		setServerDifficulty(guildId, difficulty)
		bot.MessageReactionAdd(m.Message.ChannelID, m.Message.ID, "✅")
	} else {
		bot.MessageReactionAdd(m.Message.ChannelID, m.Message.ID, "❌")
	}
}
