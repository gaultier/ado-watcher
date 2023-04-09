## Azure DevOps PR watcher

Terminal script to watch pull requests ('PR') in Azure DevOps and notify if something changed, such as:
- New PR
- PR status changed (e.g. abandoned, completed)
- New comment/thread
- Comment content changed
- Thread status changed

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
        Repositories of interest (comma separated)
  -token-path string
        Path to a file containing an access token for Azure DevOps
  -user string
        User to log in with
  -users string
        Users of interest (comma separated). PRs whose creator or reviewers match at least one of those will be shown
```
