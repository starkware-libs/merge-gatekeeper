package main

import (
	"context"
	"fmt"
	"os"

	"github.com/google/go-github/v38/github"
	"golang.org/x/oauth2"
)

func main() {
	ctx := context.Background()
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		fmt.Println("No GITHUB_TOKEN")
		return
	}
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)

	opts := &github.ListCheckRunsOptions{
		Filter: github.String("all"),
	}
	runs, _, err := client.Checks.ListCheckRunsForRef(ctx, "starkware-libs", "sequencer", "e27c06c2be867ae09efb660e5d5aff860dd54178", opts)
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, run := range runs.CheckRuns {
		suiteID := int64(0)
		if run.CheckSuite != nil && run.CheckSuite.ID != nil {
			suiteID = *run.CheckSuite.ID
		}
		appName := "unknown"
		if run.App != nil && run.App.Name != nil {
			appName = *run.App.Name
		}
		status := "unknown"
		if run.Status != nil {
			status = *run.Status
		}
		fmt.Printf("App: %s, SuiteID: %d, ID: %d, Name: %s, Status: %s\n", appName, suiteID, *run.ID, *run.Name, status)
	}
}
