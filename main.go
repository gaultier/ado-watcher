package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"golang.org/x/exp/slices"
)

type ApiResponse[T any] struct {
	Value T `json:"value"`
}

type PullRequest struct {
	Id            uint64   `json:"pullRequestId"`
	CreatedBy     Person   `json:"createdBy"`
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	Reviewers     []Person `json:"reviewers"`
	Status        string   `json:"status"`
	SourceRefName string   `json:"sourceRefName"`
	TargetRefName string   `json:"targetRefName"`
}

type Person struct {
	DisplayName string `json:"displayName"`
	UniqueName  string `json:"uniqueName"`
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

func tickPullRequestThreads(baseUrl string, repository Repository, pullRequest PullRequest, threadsDb map[uint64]Thread) {
	threads, err := fetchPullRequestThreads(baseUrl, repository.Id, pullRequest.Id)
	if err != nil {
		log.Err(err).Str("repositoryName", repository.Name).Uint64("pullRequestId", pullRequest.Id).Msg("Failed to fetch pull request threads")
		return
	}

	for _, newThread := range threads {
		if newThread.Status == nil { // Skip threads without a status
			continue
		}

		oldThread, present := threadsDb[newThread.Id]
		if !present {
			log.Info().Str("repositoryName", repository.Name).Uint64("pullRequestId", pullRequest.Id).Str("newThreadStatus", Str(newThread.Status)).Msg("New thread")
			threadsDb[newThread.Id] = newThread
		} else if *oldThread.Status != *newThread.Status {
			log.Info().Str("repositoryName", repository.Name).Uint64("pullRequestId", pullRequest.Id).Str("newThreadStatus", Str(newThread.Status)).Str("oldThreadStatus", Str(oldThread.Status)).Msg("Thread status changed")
		}

		threadsDb[newThread.Id] = newThread // Update data to be able to diff later

		for _, newComment := range newThread.Comments {
			if newComment.Type == "system" { // Skip automated comments
				continue
			}

			if oldCommentIdx := slices.IndexFunc(oldThread.Comments, func(c Comment) bool { return c.Id == newComment.Id }); oldCommentIdx != -1 {
				oldComment := &oldThread.Comments[oldCommentIdx]

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

// TODO: stop watching abandoned/completed PRs (status=abandoned|completed)
func pollPullRequest(baseUrl string, repository Repository, pullRequest PullRequest, watcher PullRequestWatcher, interval time.Duration) {
	log.Info().Str("repositoryName", repository.Name).Uint64("pullRequestId", pullRequest.Id).Str("author", pullRequest.CreatedBy.DisplayName).Str("title", pullRequest.Title).Str("description", pullRequest.Description).Str("status", pullRequest.Status).Str("sourceRefName", pullRequest.SourceRefName).Str("targetRefName", pullRequest.TargetRefName).Msg("Watching PR")

	threadsDb := make(map[uint64]Thread, 10)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	tickPullRequestThreads(baseUrl, repository, pullRequest, threadsDb)
	for {
		select {
		case <-watcher.stop:
			log.Info().Str("repositoryName", repository.Name).Uint64("pullRequestId", pullRequest.Id).Str("author", pullRequest.CreatedBy.DisplayName).Str("title", pullRequest.Title).Str("reason", "abandoned or completed").Msg("Stop watching PR")
			return
		case <-ticker.C:
			tickPullRequestThreads(baseUrl, repository, pullRequest, threadsDb)
		}
	}
}

type PullRequestWatcher struct {
	stop chan struct{}
}

func pollRepository(baseUrl string, repository Repository, peopleOfInterestUniqueNames []string, interval time.Duration) {
	log.Info().Str("repositoryName", repository.Name).Msg("Watching repository")

	pullRequestsToWatch := make(map[uint64]PullRequestWatcher, 5)

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
				pullRequestsToWatch[pullRequest.Id] = PullRequestWatcher{stop: make(chan struct{})}

				go pollPullRequest(baseUrl, repository, pullRequest, pullRequestsToWatch[pullRequest.Id], interval)
			}
		}

		// Detect abandoned/completed PRs
	found:
		for id, watching := range pullRequestsToWatch {
			for _, pullRequest := range pullRequests {
				if pullRequest.Id == id {
					continue found
				}
			}

			// TODO: should we query the PR one last time to get its final status?
			close(watching.stop)
			delete(pullRequestsToWatch, id)
		}
	}
}

func isRepositoryOfInterest(repository *Repository, repositoriesOfInterestNames []string) bool {
	if len(repositoriesOfInterestNames) == 0 {
		return true
	}
	return slices.Contains(repositoriesOfInterestNames, repository.Name)
}

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	organization := flag.String("organization", "", "Organization on Azure DevOps")
	projectId := flag.String("project", "", "Project id on Azure DevOps")
	user := flag.String("user", "", "User to log in with")
	tokenPath := flag.String("token-path", "", "Path to a file containing an access token for Azure DevOps")
	// Optional
	users := flag.String("users", "", "Users of interest (comma separated). PRs whose creator or reviewers match at least one of those will be shown. If empty, all PRs will be watched.")
	// Optional
	repositoriesNames := flag.String("repositories", "", "Repositories of interest (comma separated). If empty, all repositories will be watched.")
	interval := flag.Duration("interval", 10*time.Second, "Poll interval")

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
	tokenStr := strings.TrimSpace(string(token))

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
