## Azure DevOps PR watcher

Terminal script to watch pull requests ('PR') in Azure DevOps and notify if something changed, such as:
- New PR
- PR status changed (e.g. abandoned, completed)
- New comment/thread
- Comment content changed
- Thread status changed
- Reviewer vote added or changed (i.e. approved, rejected, etc)
- New commit pushed

It uses a [Personal Access Token (PAT)](https://learn.microsoft.com/en-us/azure/devops/organizations/accounts/use-personal-access-tokens-to-authenticate?toc=%2Fazure%2Fdevops%2Forganizations%2Fsecurity%2Ftoc.json&view=azure-devops&tabs=Windows), typically created in the UI, to authenticate to the REST API, which it polls continuously, and logs when a change is detected.


## Quick Start

First, create a PAT as mentioned above. It only needs to be read-only.

```sh
$ go build .
$ ./ado -h
Usage of ./ado:
  -interval duration
    	Poll interval (default 10s)
  -organization string
    	Organization on Azure DevOps
  -project string
    	Project id on Azure DevOps
  -repositories string
    	Repositories of interest (comma separated). If empty, all repositories will be watched.
  -token-path string
    	Path to a file containing an access token for Azure DevOps
  -user string
    	User to log in with
  -users string
    	Users of interest (comma separated). PRs whose creator or reviewers match at least one of those will be shown. If empty, all PRs will be watched.
```

Excerpt:

```
[...]
11:01AM INF Watching repository repositoryId=01817ec8-26a3-42e6-9267-0b7506938f23 repositoryName=xxx
2:55PM INF PR has new commit(s) author="Gaultier Philippe" newCommit=baed70d922e4b2088620013f1edee826b514e418 oldCommit=9413a95111eaaef67c3ec5a37596c7d16a74cec8 pullRequestId=928 repositoryName=xxx title="e2e tests"
3:05PM INF Thread status changed newThreadStatus=fixed oldThreadStatus=active pullRequestId=928 repositoryName=xxx threadId=4748
[...]
```

## Discarded evolutions

- Using WebSockets. While Azure DevOps technically allows monitoring resources over WebSocket, the implications are non-trivial (the PAT requires more permissions, the organization id needs to be known by the user, etc), and the result is not a big gain: we still need to poll (with most WebSocket Go APIs), the format is different and arcane, and each resource needs to be actively monitored (i.e. each pull request and each thread of each pull request). See the `ws` branch.
