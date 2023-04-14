package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"nhooyr.io/websocket"

	"golang.org/x/exp/slices"
)

const (
	voteApproved                = 10
	voteRejected                = -10
	voteAbsent                  = 0
	voteApprovedWithSuggestions = 5
)

var voteToString = map[int64]string{
	voteApproved:                "approved",
	voteRejected:                "rejected",
	voteAbsent:                  "no vote",
	voteApprovedWithSuggestions: "approved with suggestions",
}

type Commit struct {
	CommitId string `json:"commitId"`
}

type ApiResponse[T any] struct {
	Value T `json:"value"`
}

type PullRequest struct {
	Id                    uint64   `json:"pullRequestId"`
	CreatedBy             Person   `json:"createdBy"`
	Title                 string   `json:"title"`
	Description           string   `json:"description"`
	Reviewers             []Person `json:"reviewers"`
	Status                string   `json:"status"`
	SourceRefName         string   `json:"sourceRefName"`
	TargetRefName         string   `json:"targetRefName"`
	LastMergeSourceCommit Commit   `json:"lastMergeSourceCommit"`
}

type Person struct {
	Id          string `json:"id"`
	DisplayName string `json:"displayName"`
	UniqueName  string `json:"uniqueName"`
	Vote        int64  `json:"vote"`
}

type Repository struct {
	Id   string `json:"id"`
	Name string `json:"name"`
}

type Thread struct {
	Id       uint64    `json:"id"`
	Status   *string   `json:"status"`
	Comments []Comment `json:"comments"`
}

type Comment struct {
	Id      uint64  `json:"id"`
	Content *string `json:"content"`
	Author  Person  `json:"author"`
	Type    string  `json:"commentType"`
	// IsDeleted *bool   `json:"isDeleted"`
}

func fetchRepositories(baseUrl string) ([]Repository, error) {
	url := fmt.Sprintf("%s/git/repositories", baseUrl)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	decoder := json.NewDecoder(resp.Body)
	apiResponse := ApiResponse[[]Repository]{}
	if err := decoder.Decode(&apiResponse); err != nil {
		return nil, err
	}

	return apiResponse.Value, nil
}

func fetchRepositoryPullRequests(baseUrl string, repositoryId string) ([]PullRequest, error) {
	url := fmt.Sprintf("%s/git/repositories/%s/pullRequests", baseUrl, repositoryId)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	decoder := json.NewDecoder(resp.Body)
	apiResponse := ApiResponse[[]PullRequest]{}
	if err := decoder.Decode(&apiResponse); err != nil {
		return nil, err
	}

	return apiResponse.Value, nil
}

func fetchPullRequest(baseUrl string, repositoryId string, pullRequestId uint64) (*PullRequest, error) {
	url := fmt.Sprintf("%s/git/repositories/%s/pullRequests/%d", baseUrl, repositoryId, pullRequestId)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	decoder := json.NewDecoder(resp.Body)
	pullRequest := PullRequest{}
	if err := decoder.Decode(&pullRequest); err != nil {
		return nil, err
	}

	return &pullRequest, nil
}

func fetchPullRequestThreads(baseUrl string, repositoryId string, pullRequestId uint64) ([]Thread, error) {
	url := fmt.Sprintf("%s/git/repositories/%s/pullRequests/%d/threads", baseUrl, repositoryId, pullRequestId)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	decoder := json.NewDecoder(resp.Body)
	apiResponse := ApiResponse[[]Thread]{}
	if err := decoder.Decode(&apiResponse); err != nil {
		return nil, err
	}

	return apiResponse.Value, nil
}

func isPullRequestOfInterest(pullRequest *PullRequest, peopleOfInterestUniqueNames []string) bool {
	if len(peopleOfInterestUniqueNames) == 0 {
		return true
	}

	if slices.Contains(peopleOfInterestUniqueNames, pullRequest.CreatedBy.UniqueName) {
		return true
	}

	// Check intersection of the two slices
	for _, person := range peopleOfInterestUniqueNames {
		for _, reviewer := range pullRequest.Reviewers {
			if person == reviewer.UniqueName {
				return true
			}
		}
	}
	return false
}

func Str(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func diffPullRequestVotes(repository *Repository, localPullRequest *PullRequest, latestPullRequest *PullRequest) {
	for _, latestReviewer := range latestPullRequest.Reviewers {
		// Skip uninteresting votes.
		// Although one interesting case would be when a new commit is pushed for an approved PR, the vote is reset and the approver needs to re-approve.
		if latestReviewer.Vote == voteAbsent {
			continue
		}

		localVote := voteAbsent
		var localReviewer *Person
		if localPullRequest != nil {
			if idx := slices.IndexFunc(localPullRequest.Reviewers, func(p Person) bool { return p.Id == latestReviewer.Id }); idx != -1 {
				localReviewer = &localPullRequest.Reviewers[idx]
				localVote = int(localReviewer.Vote)
			}
		}

		if localVote == int(latestReviewer.Vote) { // Same vote, uninteresting
			continue
		}

		if localReviewer != nil { // Existing reviewer updated its vote 'interestingly'
			log.Info().Str("repositoryName", repository.Name).Uint64("pullRequestId", latestPullRequest.Id).Str("author", latestPullRequest.CreatedBy.DisplayName).Str("title", latestPullRequest.Title).Str("oldReviewerVote", voteToString[localReviewer.Vote]).Str("newReviewerVote", voteToString[latestReviewer.Vote]).Str("reviewerName", latestReviewer.DisplayName).Msg("PR has an updated reviewer vote")
		} else { // Newly added reviewer voted 'interestingly'
			log.Info().Str("repositoryName", repository.Name).Uint64("pullRequestId", latestPullRequest.Id).Str("author", latestPullRequest.CreatedBy.DisplayName).Str("title", latestPullRequest.Title).Str("reviewerVote", voteToString[latestReviewer.Vote]).Str("reviewerName", latestReviewer.DisplayName).Msg("PR has a new reviewer vote")

		}
	}
}

func tickPullRequestThreads(baseUrl string, repository Repository, pullRequest PullRequest, threadsDb map[uint64]Thread) {
	threads, err := fetchPullRequestThreads(baseUrl, repository.Id, pullRequest.Id)
	if err != nil {
		log.Err(err).Str("repositoryName", repository.Name).Uint64("pullRequestId", pullRequest.Id).Msg("Failed to fetch pull request threads")
		return
	}

	for _, latestThread := range threads {
		if latestThread.Status == nil { // Skip threads without a status
			continue
		}

		localThread, present := threadsDb[latestThread.Id]
		if !present {
			log.Info().Str("repositoryName", repository.Name).Uint64("pullRequestId", pullRequest.Id).Str("newThreadStatus", Str(latestThread.Status)).Uint64("threadId", latestThread.Id).Msg("New thread")
			threadsDb[latestThread.Id] = latestThread
		} else if Str(localThread.Status) != Str(latestThread.Status) {
			log.Info().Str("repositoryName", repository.Name).Uint64("pullRequestId", pullRequest.Id).Str("newThreadStatus", Str(latestThread.Status)).Str("oldThreadStatus", Str(localThread.Status)).Uint64("threadId", latestThread.Id).Msg("Thread status changed")
		}

		threadsDb[latestThread.Id] = latestThread // Upsert data to be able to diff later

		for _, newComment := range latestThread.Comments {
			if newComment.Type == "system" { // Skip automated comments
				continue
			}

			if oldCommentIdx := slices.IndexFunc(localThread.Comments, func(c Comment) bool { return c.Id == newComment.Id }); oldCommentIdx != -1 {
				oldComment := &localThread.Comments[oldCommentIdx]

				if Str(oldComment.Content) != Str(newComment.Content) {
					log.Info().Str("repositoryName", repository.Name).Uint64("pullRequestId", pullRequest.Id).Str("author", newComment.Author.DisplayName).Str("oldContent", Str(oldComment.Content)).Str("newContent", Str(newComment.Content)).Msg("Updated comment")
					continue
				}
			} else {
				log.Info().Str("repositoryName", repository.Name).Uint64("pullRequestId", pullRequest.Id).Str("author", newComment.Author.DisplayName).Str("content", Str(newComment.Content)).Msg("New comment")
				continue
			}
		}
	}
}

func pollPullRequest(baseUrl string, repository Repository, pullRequestId uint64, watcher PullRequestWatcher, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	contextToken := uuid.New()
	u := fmt.Sprintf("https://%s:%s@dev.azure.com/advance52/_apis/88750e91-80b9-448b-932d-a55973705c13/signalr/negotiate?clientProtocol=1.5&contextToken=%s", *user, tokenStr, contextToken.String())
	resp, err := http.Get(u)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	decoder := json.NewDecoder(resp.Body)
	obj := map[string]any{}
	err = decoder.Decode(&obj)
	if err != nil {
		panic(err)
	}

	tok := obj["ConnectionToken"].(string)

	u = fmt.Sprintf("wss://%s:%s@dev.azure.com/advance52/_apis/88750e91-80b9-448b-932d-a55973705c13/signalr/connect?transport=webSockets&clientProtocol=1.5&connectionToken=%s", *user, tokenStr, url.QueryEscape(tok))
	u += "&connectionData=%5B%7B%22name%22%3A%22pullrequestdetailhub%22%7D%5D&tid=6"

	log.Printf("u=%s", u)

	c, resp, err := websocket.Dial(context.TODO(), u, nil)
	if err != nil {
		r, _ := io.ReadAll(resp.Body)
		log.Printf("resp=%s", r)
		panic(err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	err = c.Write(context.TODO(), websocket.MessageText, []byte(`{"H":"pullrequestdetailhub","M":"Subscribe","A":[928,"bdb14414-a0a7-41ae-a834-b9230582bf38"],"I":0}`))
	if err != nil {
		panic(err)
	}

	for ; true; <-ticker.C {
		msgType, msg, err := c.Read(context.TODO())
		if err != nil {
			panic(err)
		}
		log.Debug().Any("msgType", msgType).Str("msg", string(msg)).Msg("read")
	}
}

func pollPullRequestAndThreads(baseUrl string, repository Repository, pullRequest PullRequest, interval time.Duration) {
	log.Info().Str("repositoryName", repository.Name).Uint64("pullRequestId", pullRequest.Id).Str("author", pullRequest.CreatedBy.DisplayName).Str("title", pullRequest.Title).Str("description", pullRequest.Description).Str("status", pullRequest.Status).Str("sourceRefName", pullRequest.SourceRefName).Str("targetRefName", pullRequest.TargetRefName).Msg("Watching PR")

	watcher := PullRequestWatcher{stop: make(chan struct{})}
	go pollPullRequest(baseUrl, repository, pullRequest.Id, watcher, interval)

	threadsDb := make(map[uint64]Thread, 10)

	threadsTicker := time.NewTicker(interval)
	defer threadsTicker.Stop()

	tickPullRequestThreads(baseUrl, repository, pullRequest, threadsDb)
	for {
		select {
		case <-watcher.stop:
			return
		case <-threadsTicker.C:
			tickPullRequestThreads(baseUrl, repository, pullRequest, threadsDb)
		}
	}
}

type PullRequestWatcher struct {
	stop chan struct{}
}

func pollRepository(baseUrl string, repository Repository, peopleOfInterestUniqueNames []string, interval time.Duration) {
	log.Info().Str("repositoryName", repository.Name).Str("repositoryId", repository.Id).Msg("Watching repository")

	pullRequestsToWatch := make(map[uint64]struct{}, 5)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for ; true; <-ticker.C {
		pullRequests, err := fetchRepositoryPullRequests(baseUrl, repository.Id)
		if err != nil {
			log.Err(err).Str("repositoryName", repository.Name).Msg("Failed to fetch PRs")
			continue
		}

		for _, pullRequest := range pullRequests {
			_, present := pullRequestsToWatch[pullRequest.Id]
			// Start watching
			if !present && isPullRequestOfInterest(&pullRequest, peopleOfInterestUniqueNames) {
				pullRequestsToWatch[pullRequest.Id] = struct{}{}

				go pollPullRequestAndThreads(baseUrl, repository, pullRequest, interval)
			}
		}

		// Remove abandoned/completed PRs from `pullRequestsToWatch` left intentionally out for simplicity.
	}
}

func isRepositoryOfInterest(repository *Repository, repositoriesOfInterestNames []string) bool {
	if len(repositoriesOfInterestNames) == 0 {
		return true
	}
	return slices.Contains(repositoriesOfInterestNames, repository.Name)
}

var organization = flag.String("organization", "", "Organization on Azure DevOps")
var projectId = flag.String("project", "", "Project id on Azure DevOps")
var user = flag.String("user", "", "User to log in with")
var tokenPath = flag.String("token-path", "", "Path to a file containing an access token for Azure DevOps")

// Optional
var users = flag.String("users", "", "Users of interest (comma separated). PRs whose creator or reviewers match at least one of those will be shown. If empty, all PRs will be watched.")

// Optional
var repositoriesNames = flag.String("repositories", "", "Repositories of interest (comma separated). If empty, all repositories will be watched.")
var interval = flag.Duration("interval", 10*time.Second, "Poll interval")
var tokenStr = ""

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	flag.Parse()

	if *organization == "" || *projectId == "" || *user == "" || *tokenPath == "" {
		flag.Usage()
		os.Exit(1)
	}

	peopleOfInterestUniqueNames := strings.Split(*users, ",")
	if *users == "" {
		peopleOfInterestUniqueNames = []string{}
	}
	repositoriesOfInterestNames := strings.Split(*repositoriesNames, ",")
	if *repositoriesNames == "" {
		repositoriesOfInterestNames = []string{}
	}

	token, err := os.ReadFile(*tokenPath)
	if err != nil {
		log.Fatal().Err(err).Str("path", *tokenPath).Msg("Failed to read file")
	}
	tokenStr = strings.TrimSpace(string(token))

	baseUrl :=
		fmt.Sprintf("https://%s:%s@dev.azure.com/%s/%s/_apis", *user, tokenStr, *organization, *projectId)
	repositories, err := fetchRepositories(baseUrl)

	if err != nil {
		log.Fatal().Err(err).Msg("Failed to fetch repositories")
	}

	if len(repositories) == 0 {
		log.Fatal().Msg("No repositories found")
	}

	for _, repository := range repositories {
		if isRepositoryOfInterest(&repository, repositoriesOfInterestNames) {
			go pollRepository(baseUrl, repository, peopleOfInterestUniqueNames, *interval)
		}
	}

	wait := make(chan struct{})
	<-wait
}
