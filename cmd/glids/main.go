package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"glids/internal/display"
	"glids/internal/gitlab"
)

var (
	debugLogger *log.Logger
	isDebug     bool
)

// Helper function to manage status messages with progress indicator
// Accepts a channel to pause/resume the animation
func showStatus(message string, pauseControl <-chan bool) func() { // Added pauseControl parameter
	progressChars := []string{"|", "/", "-", "\\"}
	progressIndex := 0
	ticker := time.NewTicker(100 * time.Millisecond)
	done := make(chan bool)
	paused := false // Added paused state

	// Function to clear the status line
	clearLine := func() {
		// Ensure enough spaces to clear the line, including the animation character and space
		fmt.Fprintf(os.Stderr, "\r%s\r", strings.Repeat(" ", len(message)+3))
	}

	// Start goroutine to animate progress
	go func() {
		defer close(done)
		defer clearLine() // Ensure line is cleared when goroutine exits

		for {
			select {
			case <-ticker.C:
				if !paused { // Only animate if not paused
					// Print status with animation character
					fmt.Fprintf(os.Stderr, "\r%s %s", message, progressChars[progressIndex%len(progressChars)])
					progressIndex++
				}
			case p, ok := <-pauseControl: // Listen for pause signals
				if !ok { // Channel closed, treat as done
					return
				}
				paused = p
				if paused {
					clearLine() // Clear the line when pausing
				} else {
					// Optional: Immediately print status without animation char when resuming
					// This helps show that something is still happening.
					fmt.Fprintf(os.Stderr, "\r%s  ", message) // Two spaces to overwrite animation char
				}
			case <-done:
				ticker.Stop() // Stop the ticker explicitly
				return        // Exit goroutine
			}
		}
	}()

	// Return the function to stop the animation
	return func() {
		select {
		case <-done: // Already closed
			return
		default:
			close(done) // Signal the goroutine to stop
		}
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

	// --- Create Pause Channel ---
	// Use a buffered channel to prevent potential blocking if the signal is sent
	// before the receiver in showStatus is ready, although unlikely with current setup.
	pauseCh := make(chan bool, 1)

	// Create GitLab client, passing the pause channel
	client := gitlab.NewClient(baseURL, gitlabToken, debugLogger, pauseCh)

	var clearStatus func() = func() {} // No-op clear function initially

	// --- Execution Logic ---
	// Determine the initial status message based on the mode
	statusMessage := "Fetching data..."
	if *showHierarchy {
		statusMessage = "Fetching initial groups for hierarchy..."
	} else if *showGroups {
		statusMessage = "Fetching groups..."
	} else {
		statusMessage = "Fetching projects..."
	}

	// Start status only if not in debug mode (debug logs interfere anyway)
	if !isDebug {
		// Pass the pause channel to showStatus
		clearStatus = showStatus(statusMessage, pauseCh)
	}

	// Select mode and run
	if *showHierarchy {
		runHierarchyMode(client, *searchTerm, *allItems, clearStatus, pauseCh) // Pass pauseCh for potential restarts
	} else if *showGroups {
		runGroupsMode(client, *searchTerm, *allItems, clearStatus)
	} else {
		runProjectsMode(client, *searchTerm, *allItems, clearStatus)
	}

	// clearStatus() // This is now handled by the defer in each run*Mode function
}

// Pass pauseCh to runHierarchyMode in case we want to restart status during population
func runHierarchyMode(client *gitlab.Client, searchTerm string, allItems bool, clearStatus func(), pauseCh chan bool) {
	defer clearStatus() // Stops the initial status animation when the function exits

	debugLogger.Printf("Running in hierarchy mode, search term: '%s'", searchTerm)

	// Fetch initial matching groups (roots of the trees)
	// The confirmation logic (including pausing) is now inside GetGroups
	matchingGroups, err := client.GetGroups(searchTerm, allItems)
	if err != nil {
		// clearStatus() is handled by defer
		// Check if error is cancellation
		if err.Error() == "operation cancelled by user" {
			fmt.Println("\nOperation cancelled.") // Give user feedback
			os.Exit(0)                            // Exit cleanly after cancellation
		}
		// Print other errors on a new line
		fmt.Fprintf(os.Stderr, "\nError getting initial groups: %v\n", err)
		os.Exit(1)
	}
	debugLogger.Printf("Found %d initial matching groups", len(matchingGroups))

	// Defer handles clearing the status line now.

	if len(matchingGroups) == 0 {
		fmt.Println("\nNo groups found matching search term:", searchTerm)
		return // Exit gracefully
	}

	// Sort the initial matching groups by path for consistent output order
	sort.Slice(matchingGroups, func(i, j int) bool {
		return strings.ToLower(matchingGroups[i].FullPath) < strings.ToLower(matchingGroups[j].FullPath)
	})

	fmt.Println("\nPopulating hierarchy for found groups...") // Indicate next step

	// --- Populate Hierarchy ---
	// We won't restart the main status indicator here to avoid complexity.
	// If population takes a long time, the user just won't see the spinner.
	// Confirmation for large subgroups/projects *within* PopulateGroupHierarchy
	// will still pause/resume correctly using the original pauseCh passed to the client.

	populatedGroups := make([]gitlab.Group, 0, len(matchingGroups))
	populationCancelled := false // Flag to track if cancellation happened during population
	const statusClearWidth = 80 // Width for clearing status lines

	// --- Population Loop ---
	for i, group := range matchingGroups {
		// Print status update for the current group BEFORE processing
		statusLine := fmt.Sprintf("[%d/%d] Populating: %s...", i+1, len(matchingGroups), group.FullPath)
		// Print status, pad with spaces to overwrite previous longer lines, use \r
		fmt.Fprintf(os.Stderr, "\r%-*s", statusClearWidth, statusLine)

		rootGroup := group // Make a copy
		err := client.PopulateGroupHierarchy(&rootGroup, allItems)

		if err != nil {
			// Clear the status line before printing error/warning/cancellation
			fmt.Fprint(os.Stderr, "\r"+strings.Repeat(" ", statusClearWidth)+"\r")

			// Check if error is cancellation from within PopulateGroupHierarchy
			if err.Error() == "operation cancelled by user" {
				fmt.Println("\nOperation cancelled during hierarchy population.")
				populationCancelled = true
				// We break here because the user explicitly cancelled.
				// We'll still print whatever was populated before cancellation.
				break // Exit the loop
			}
			// Log other errors but continue with other groups
			fmt.Fprintf(os.Stderr, "\nWarning: Failed to fully populate group %s (ID: %d): %v\n", rootGroup.FullPath, rootGroup.ID, err)
			// Continue processing other groups, but add the partially populated one
		}
		// Add fully or partially populated groups (unless cancelled)
		populatedGroups = append(populatedGroups, rootGroup)

		// REMOVED: The visual separator print block
	}
	// --- End Population Loop ---

	// Clear the final status line from the loop before printing results
	fmt.Fprint(os.Stderr, "\r"+strings.Repeat(" ", statusClearWidth)+"\r")

	// --- Print Results ---
	if len(populatedGroups) > 0 {
		// Ensure the header starts on a new line if the previous line was just cleared
		fmt.Println("\n--- Hierarchy ---") // Header for the results
		for _, group := range populatedGroups {
			display.PrintHierarchy(group)
		}
	} else if !populationCancelled { // Only print "no groups" if not cancelled
		// Ensure this message starts on a new line
		fmt.Println("\nNo groups found or populated.")
	}

	// If cancelled during population, exit cleanly now
	if populationCancelled {
		os.Exit(0)
	}
}

func runGroupsMode(client *gitlab.Client, searchTerm string, allItems bool, clearStatus func()) {
	defer clearStatus() // Stops status animation on exit

	debugLogger.Printf("Running in groups mode, search term: '%s'", searchTerm)
	groups, err := client.GetGroups(searchTerm, allItems)
	if err != nil {
		// clearStatus() handled by defer
		if err.Error() == "operation cancelled by user" {
			fmt.Println("\nOperation cancelled.")
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "\nError getting groups: %v\n", err)
		os.Exit(1)
	}
	debugLogger.Printf("Found %d groups", len(groups))

	// Defer handles clearing the status line now.

	if len(groups) == 0 {
		fmt.Println("\nNo groups found matching search term:", searchTerm)
		return
	}

	// Sort groups by path name before display
	sort.Slice(groups, func(i, j int) bool {
		return strings.ToLower(groups[i].FullPath) < strings.ToLower(groups[j].FullPath)
	})

	fmt.Println("\n--- Groups ---") // Header for the results
	display.PrintGroupList(groups)
}

func runProjectsMode(client *gitlab.Client, searchTerm string, allItems bool, clearStatus func()) {
	defer clearStatus() // Stops status animation on exit

	debugLogger.Printf("Running in projects mode, search term: '%s'", searchTerm)
	projects, err := client.GetProjects(searchTerm, allItems)
	if err != nil {
		// clearStatus() handled by defer
		if err.Error() == "operation cancelled by user" {
			fmt.Println("\nOperation cancelled.")
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "\nError getting projects: %v\n", err)
		os.Exit(1)
	}
	debugLogger.Printf("Found %d projects", len(projects))

	// Defer handles clearing the status line now.

	if len(projects) == 0 {
		fmt.Println("\nNo projects found matching search term:", searchTerm)
		return
	}

	// Sort projects by path name before display
	sort.Slice(projects, func(i, j int) bool {
		return strings.ToLower(projects[i].PathWithNamespace) < strings.ToLower(projects[j].PathWithNamespace)
	})

	fmt.Println("\n--- Projects ---") // Header for the results
	display.PrintProjectList(projects)
}
