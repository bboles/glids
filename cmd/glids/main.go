package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"

	"glids/internal/display"
	"glids/internal/gitlab"
)

var (
	debugLogger *log.Logger
	isDebug     bool
)

// Helper function to manage status messages
func showStatus(message string) func() {
	fmt.Fprint(os.Stderr, message+"\r")
	// Return a function that clears the status
	return func() {
		// Overwrite with spaces and return cursor to beginning
		fmt.Fprint(os.Stderr, "\r"+strings.Repeat(" ", len(message)+5)+"\r")
	}
}

func main() {
	// --- Configuration and Setup ---
	searchTerm := flag.String("search", "", "Search term to filter projects or groups")
	allItems := flag.Bool("all", false, "List all projects/groups regardless of activity date")
	showGroups := flag.Bool("groups", false, "Show groups and subgroups instead of projects")
	showHierarchy := flag.Bool("hierarchy", false, "Show groups, subgroups, and projects in hierarchical format")
	serverFlag := flag.String("server", "", "GitLab server host (e.g., gitlab.example.com). Overrides GITLAB_HOST env var.")
	debug := flag.Bool("debug", false, "Enable debug logging")
	flag.Parse()

	// Setup debug logging
	isDebug = *debug
	if isDebug {
		debugLogger = log.New(os.Stderr, "[DEBUG] ", log.Ltime|log.Lshortfile)
		debugLogger.Println("Debug logging enabled")
	} else {
		// Provide a discard logger even when debug is off, so internal packages don't panic
		debugLogger = log.New(io.Discard, "", 0)
	}

	// Get positional arguments as search term if provided
	if flag.NArg() > 0 {
		*searchTerm = flag.Arg(0)
		debugLogger.Printf("Using positional argument for search term: %s", *searchTerm)
	}

	// Determine GitLab host: prioritize flag, then env var
	gitlabHost := *serverFlag
	if gitlabHost == "" {
		debugLogger.Println("Server flag not provided, checking GITLAB_HOST environment variable.")
		gitlabHost = os.Getenv("GITLAB_HOST")
		if gitlabHost != "" {
			debugLogger.Printf("Using GitLab Host from GITLAB_HOST env var: %s", gitlabHost)
		}
	} else {
		debugLogger.Printf("Using GitLab Host from --server flag: %s", gitlabHost)
	}

	// Get token and validate host
	gitlabToken := os.Getenv("GITLAB_TOKEN")
	if gitlabToken == "" || gitlabHost == "" {
		fmt.Fprintln(os.Stderr, "Error: GITLAB_TOKEN environment variable must be set, and GitLab host must be provided via --server flag or GITLAB_HOST environment variable.")
		os.Exit(1)
	}

	// Construct base URL assuming HTTPS
	baseURL := "https://" + gitlabHost

	// Create GitLab client
	client := gitlab.NewClient(baseURL, gitlabToken, debugLogger)

	// --- Execution Logic ---
	if *showHierarchy {
		runHierarchyMode(client, *searchTerm, *allItems)
	} else if *showGroups {
		runGroupsMode(client, *searchTerm, *allItems)
	} else {
		runProjectsMode(client, *searchTerm, *allItems)
	}
}

func runHierarchyMode(client *gitlab.Client, searchTerm string, allItems bool) {
	debugLogger.Printf("Running in hierarchy mode, search term: '%s'", searchTerm)

	// Fetch initial matching groups (roots of the trees)
	matchingGroups, err := client.GetGroups(searchTerm, allItems)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting initial groups: %v\n", err)
		os.Exit(1)
	}
	debugLogger.Printf("Found %d initial matching groups", len(matchingGroups))

	if len(matchingGroups) == 0 {
		fmt.Println("No groups found matching search term:", searchTerm)
		return // Exit gracefully
	}

	// Sort the initial matching groups by path for consistent output order
	sort.Slice(matchingGroups, func(i, j int) bool {
		return strings.ToLower(matchingGroups[i].FullPath) < strings.ToLower(matchingGroups[j].FullPath)
	})

	// Populate and print hierarchy for each root group
	for i, group := range matchingGroups {
		// Create a modifiable copy for population
		rootGroup := group
		err := client.PopulateGroupHierarchy(&rootGroup, allItems)
		if err != nil {
			// Log the error for this specific root, but continue with others
			fmt.Fprintf(os.Stderr, "Warning: Error building hierarchy for group %s (ID: %d): %v\n", rootGroup.FullPath, rootGroup.ID, err)
			// Optionally print the partially populated group or skip it
			// display.PrintHierarchy(rootGroup) // Could print what was fetched
			continue
		}

		// Print the fully populated hierarchy for this root
		display.PrintHierarchy(rootGroup)

		// Add a visual separator if there are multiple root groups
		if i < len(matchingGroups)-1 {
			fmt.Println(strings.Repeat("-", 40))
		}
	}
}

func runGroupsMode(client *gitlab.Client, searchTerm string, allItems bool) {
	debugLogger.Printf("Running in groups mode, search term: '%s'", searchTerm)
	groups, err := client.GetGroups(searchTerm, allItems)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting groups: %v\n", err)
		os.Exit(1)
	}
	debugLogger.Printf("Found %d groups", len(groups))

	// Sort groups by path name before display
	sort.Slice(groups, func(i, j int) bool {
		return strings.ToLower(groups[i].FullPath) < strings.ToLower(groups[j].FullPath)
	})

	display.PrintGroupList(groups)
}

func runProjectsMode(client *gitlab.Client, searchTerm string, allItems bool) {
	debugLogger.Printf("Running in projects mode, search term: '%s'", searchTerm)
	projects, err := client.GetProjects(searchTerm, allItems)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting projects: %v\n", err)
		os.Exit(1)
	}
	debugLogger.Printf("Found %d projects", len(projects))

	// Sort projects by path name before display
	sort.Slice(projects, func(i, j int) bool {
		return strings.ToLower(projects[i].PathWithNamespace) < strings.ToLower(projects[j].PathWithNamespace)
	})

	display.PrintProjectList(projects)
}
