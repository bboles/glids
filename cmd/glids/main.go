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
	"golang.org/x/term" // <-- Import term
)

var (
	debugLogger *log.Logger
	isDebug     bool
)

// Helper function to manage status messages with progress indicator
// Accepts a channel to pause/resume the animation
func showStatus(message string, pauseControl <-chan bool) func() {
	stderrFd := int(os.Stderr.Fd())
	isTerminal := term.IsTerminal(stderrFd)

	// If stderr is not a terminal, just print the message once and do nothing else.
	if !isTerminal {
		fmt.Fprintln(os.Stderr, message+"...") // Indicate work is starting
		return func() {}                       // Return a no-op closer
	}

	// --- Terminal-specific logic ---
	progressChars := []string{"|", "/", "-", "\\"}
	progressIndex := 0
	ticker := time.NewTicker(100 * time.Millisecond)
	done := make(chan bool)
	paused := false

	// Function to clear the status line using ANSI escape code
	clearLine := func() {
		fmt.Fprint(os.Stderr, "\r\x1b[K") // Carriage return, clear line to end
	}

	// Start goroutine to animate progress
	go func() {
		defer close(done)
		// Ensure line is cleared when goroutine exits (e.g., on success/completion)
		// We clear *before* printing the final state or letting the main flow continue.
		defer clearLine()

		for {
			select {
			case <-ticker.C:
				if !paused {
					clearLine() // Clear previous status
					// Print status with animation character
					fmt.Fprintf(os.Stderr, "\r%s %s", message, progressChars[progressIndex%len(progressChars)])
					progressIndex++
				}
			case p, ok := <-pauseControl: // Listen for pause signals
				if !ok { // Channel closed, treat as done
					return
				}
				paused = p
				clearLine() // Clear the line when pausing or resuming
				if !paused {
					// Optional: Immediately print status without animation char when resuming
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
			// Give the goroutine a moment to finish and clear the line via its defer
			// This prevents subsequent output potentially overwriting the status line
			// before the goroutine's defer clearLine executes.
			// Adjust timing if needed, or use a sync mechanism if more robustness is required.
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func main() {
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
	logOutput := io.Discard // Default to discard
	if isDebug {
		logOutput = os.Stderr // Use Stderr for debug logs
		// No need to create the logger yet, we might need terminal info first
	}

	// Determine if Stderr is a terminal *before* potentially overwriting debugLogger
	isStderrTerminal := term.IsTerminal(int(os.Stderr.Fd()))

	// Initialize debugLogger *after* checking terminal status
	if isDebug {
		prefix := "[DEBUG] "
		// Add extra newline if stderr is a terminal to avoid clashing with status line
		// if isStderrTerminal {
		//	 prefix = "\n" + prefix // Add newline before debug prefix if terminal
		// }
		// Decided against adding newline prefix automatically, let debug messages flow naturally.
		debugLogger = log.New(logOutput, prefix, log.Ltime|log.Lshortfile)
		debugLogger.Println("Debug logging enabled")
	} else {
		// Provide a discard logger even when debug is off
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

	// Start status only if not in debug mode AND stderr is a terminal
	if !isDebug && isStderrTerminal {
		clearStatus = showStatus(statusMessage, pauseCh)
	} else if !isDebug {
		// If not debug and not a terminal, print the initial message simply
		fmt.Fprintln(os.Stderr, statusMessage+"...")
	}
	// If debug is enabled, status indicator is skipped entirely.

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
	populatedGroups := make([]gitlab.Group, 0, len(matchingGroups))
	populationCancelled := false
	stderrFd := int(os.Stderr.Fd())
	isTerminal := term.IsTerminal(stderrFd)
	terminalWidth := 80 // Default width if not a terminal or size check fails
	if isTerminal {
		width, _, err := term.GetSize(stderrFd)
		if err == nil {
			terminalWidth = width
		} else {
			debugLogger.Printf("Warning: Could not get terminal size: %v", err)
		}
	}

	// --- Population Loop ---
	for i, group := range matchingGroups {
		// Print status update for the current group BEFORE processing
		statusLine := fmt.Sprintf("[%d/%d] Populating: %s...", i+1, len(matchingGroups), group.FullPath)
		if isTerminal {
			// Truncate status line if it's too long for the terminal width
			maxLen := terminalWidth - 1 // Leave room for cursor potentially
			if len(statusLine) > maxLen {
				// Truncate with ellipsis, ensuring space for "..."
				if maxLen > 3 {
					statusLine = statusLine[:maxLen-3] + "..."
				} else { // Very narrow terminal
					statusLine = statusLine[:maxLen]
				}
			}
			// Use ANSI clear code \r\x1b[K (carriage return, clear line)
			fmt.Fprintf(os.Stderr, "\r\x1b[K%s", statusLine)
		} else {
			// Non-terminal: just print the status line on its own line
			fmt.Fprintln(os.Stderr, statusLine)
		}

		rootGroup := group // Make a copy
		err := client.PopulateGroupHierarchy(&rootGroup, allItems)

		// Clear the status line *before* printing errors/warnings/cancellation or moving to the next item
		if isTerminal {
			fmt.Fprint(os.Stderr, "\r\x1b[K")
		}

		if err != nil {
			// Error handling remains the same, but the status line is already cleared
			if err.Error() == "operation cancelled by user" {
				fmt.Println("\nOperation cancelled during hierarchy population.")
				populationCancelled = true
				break // Exit the loop
			}
			// Print warning on a new line (status line is clear)
			fmt.Fprintf(os.Stderr, "\nWarning: Failed to fully populate group %s (ID: %d): %v\n", rootGroup.FullPath, rootGroup.ID, err)
			// Continue processing other groups
		}
		// Add fully or partially populated groups (unless cancelled)
		populatedGroups = append(populatedGroups, rootGroup)
	}
	// --- End Population Loop ---

	// Final clear of the status line (might be redundant if loop finished cleanly, but safe)
	if isTerminal {
		fmt.Fprint(os.Stderr, "\r\x1b[K")
	}

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
