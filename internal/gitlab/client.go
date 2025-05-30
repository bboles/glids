package gitlab

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term" // Added for raw terminal input
)

const largeFetchThreshold = 50 // Threshold for asking confirmation before fetching many items

// Client handles communication with the GitLab API.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
	logger     *log.Logger
	confirmFn  func(string) bool
	// Add channel to signal pausing the status animation
	pauseStatus chan<- bool // Write-only channel
}

// NewClient creates a new GitLab API client.
// Modify NewClient to accept the pause channel.
func NewClient(baseURL, token string, logger *log.Logger, pauseCh chan<- bool) *Client { // Added pauseCh parameter
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Client{
		baseURL:     baseURL,
		token:       token,
		httpClient:  &http.Client{},
		logger:      logger,
		confirmFn:   defaultConfirmFn,
		pauseStatus: pauseCh, // Store the channel
	}
}

// SetConfirmationFunction allows overriding the default confirmation function.
func (c *Client) SetConfirmationFunction(fn func(string) bool) {
	c.confirmFn = fn
}

// defaultConfirmFn prompts the user for confirmation.
// It attempts to read a single character (y/n) without requiring Enter if stdin is a terminal.
// Otherwise, it falls back to reading a line.
// It now clears the line using ANSI codes if stderr is a terminal.
func defaultConfirmFn(message string) bool {
	stderrFd := int(os.Stderr.Fd())
	isStderrTerminal := term.IsTerminal(stderrFd)

	if isStderrTerminal {
		// Clear the current line on stderr using ANSI code before printing the prompt
		fmt.Fprint(os.Stderr, "\r\x1b[K")
	} else {
		// If stderr is not a terminal (e.g., redirected to a file),
		// print a newline before the prompt to avoid messing up the output format.
		fmt.Fprintln(os.Stderr)
	}
	fmt.Fprint(os.Stderr, message+" (y/n): ") // Print prompt to stderr

	stdinFd := int(os.Stdin.Fd())
	isStdinTerminal := term.IsTerminal(stdinFd)

	// Use raw mode only if both stdin and stderr are terminals.
	// We need stderr to be a terminal to properly echo the character without messing up lines.
	if isStdinTerminal && isStderrTerminal {
		oldState, err := term.MakeRaw(stdinFd)
		if err != nil {
			// Fallback to line reading on error, print error message clearly to stderr
			fmt.Fprintln(os.Stderr, "\nError setting raw mode, please press Enter after y/n:", err)
			// Use the fallback reader which reads from Stdin
			return readLineConfirmation()
		}
		defer term.Restore(stdinFd, oldState) // Ensure terminal state is restored

		var buf [1]byte
		n, err := os.Stdin.Read(buf[:]) // Read one byte (character)
		if err != nil || n == 0 {
			fmt.Fprintln(os.Stderr, "\nError reading input:", err) // Print error to stderr
			return false                                           // Default to no on read error
		}

		// Handle Ctrl+C explicitly in raw mode (ASCII value 3)
		if buf[0] == 3 {
			fmt.Fprintln(os.Stderr, "^C\nOperation cancelled by user.") // Echo ^C and message
			return false                                                // Treat Ctrl+C as cancellation
		}

		char := strings.ToLower(string(buf[0]))
		// Echo the character followed by a newline to stderr, so it appears after the prompt.
		fmt.Fprintln(os.Stderr, string(buf[0]))

		return char == "y"
	} else {
		// Fallback for non-terminal input (stdin) or non-terminal output (stderr)
		// If stderr wasn't a terminal, the prompt is already printed (possibly after a newline).
		// If stdin wasn't a terminal, we need line reading anyway.
		return readLineConfirmation() // Reads from Stdin
	}
}

// readLineConfirmation handles the confirmation by reading a full line.
// This is used as a fallback when raw terminal input is not available.
func readLineConfirmation() bool {
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		// Print error to stderr if possible
		fmt.Fprintln(os.Stderr, "\nError reading input line:", err)
		return false // Default to no on read error
	}
	response = strings.ToLower(strings.TrimSpace(response))
	return response == "y" || response == "yes"
}

// confirmLargeFetch checks if the total number of items exceeds the threshold
// and asks the user for confirmation if it does. It returns true if the operation
// should proceed (count is below threshold or user confirmed), false otherwise.
// Now signals pause/resume via the channel.
func (c *Client) confirmLargeFetch(resourceDescription string, totalCount int) bool {
	if totalCount <= largeFetchThreshold {
		return true // No confirmation needed
	}

	// --- Signal Pause ---
	// Use a separate flag to know if we paused, so we only resume if we paused.
	didPause := false
	if c.pauseStatus != nil {
		c.logger.Printf("Signalling status pause")
		// Use non-blocking send in case the channel buffer is full or receiver isn't ready
		// Although unlikely with a buffer of 1 and the current flow.
		select {
		case c.pauseStatus <- true:
			didPause = true
			// Give a brief moment for the pause signal to be processed in showStatus
			// This helps ensure the prompt appears after the status line is cleared.
			time.Sleep(50 * time.Millisecond)
		default:
			c.logger.Printf("Warning: Failed to send pause signal (channel full or nil)")
			// Proceed without pausing if signal fails
		}
	}

	// Log message (will appear on a new line after pausing/clearing)
	c.logger.Printf("Large number of %s detected: %d", resourceDescription, totalCount)

	// Prepare and show prompt
	prompt := fmt.Sprintf("This operation will fetch %d %s. Continue?", totalCount, resourceDescription)
	confirmed := c.confirmFn(prompt)

	if confirmed {
		c.logger.Printf("User confirmed fetching %d %s", totalCount, resourceDescription)
		// --- Signal Resume ---
		if didPause && c.pauseStatus != nil { // Only resume if we paused
			c.logger.Printf("Signalling status resume")
			// Use non-blocking send
			select {
			case c.pauseStatus <- false:
				// Resume signal sent
			default:
				c.logger.Printf("Warning: Failed to send resume signal (channel full or nil)")
			}
		}
		return true // User confirmed
	} else {
		c.logger.Printf("User cancelled operation due to large fetch size (%d %s)", totalCount, resourceDescription)
		// Do NOT resume pause here. The operation is cancelled.
		// The calling function should handle the cancellation error.
		// We also don't need to explicitly clear the prompt line here,
		// as the calling function will either print an error or exit.
		return false // User cancelled
	}
}

// extractPaginationInfo extracts pagination information from response headers.
func extractPaginationInfo(resp *http.Response) *PaginationInfo {
	info := &PaginationInfo{}

	// Try to extract X-Total header
	if totalStr := resp.Header.Get("X-Total"); totalStr != "" {
		if total, err := strconv.Atoi(totalStr); err == nil {
			info.Total = total
		}
	}

	// Try to extract X-Per-Page header
	if perPageStr := resp.Header.Get("X-Per-Page"); perPageStr != "" {
		if perPage, err := strconv.Atoi(perPageStr); err == nil {
			info.PerPage = perPage
		}
	}

	// Try to extract X-Total-Pages header
	if totalPagesStr := resp.Header.Get("X-Total-Pages"); totalPagesStr != "" {
		if totalPages, err := strconv.Atoi(totalPagesStr); err == nil {
			info.TotalPages = totalPages
		}
	}

	// Try to extract X-Page header
	if pageStr := resp.Header.Get("X-Page"); pageStr != "" {
		if page, err := strconv.Atoi(pageStr); err == nil {
			info.CurrentPage = page
		}
	}

	return info
}

// Helper function for making authenticated GET requests and decoding JSON.
// Now returns pagination info alongside the error.
func (c *Client) get(url string, target interface{}) (*PaginationInfo, error) {
	c.logger.Printf("Making API request to: %s", url)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %v", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error making API request: %v", err)
	}
	defer resp.Body.Close()

	// Extract pagination information
	paginationInfo := extractPaginationInfo(resp)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return paginationInfo, fmt.Errorf("error reading response body: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		c.logger.Printf("API request failed with status %d: %s", resp.StatusCode, body)
		return paginationInfo, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, body)
	}

	err = json.Unmarshal(body, target)
	if err != nil {
		c.logger.Printf("Error parsing JSON response: %v, response body: %s", err, string(body))
		return paginationInfo, fmt.Errorf("error parsing JSON response: %v", err)
	}
	return paginationInfo, nil
}

// CheckResourceCount fetches just the first page to get total count.
func (c *Client) CheckResourceCount(resourceType string, allItems bool, searchTerm string) (int, error) {
	var url string

	switch resourceType {
	case "groups":
		url = fmt.Sprintf("%s/api/v4/groups?per_page=1&page=1&all_available=true", c.baseURL)
		if searchTerm != "" {
			url = fmt.Sprintf("%s&search=%s", url, searchTerm)
		}
	case "projects":
		url = fmt.Sprintf("%s/api/v4/projects?per_page=1&page=1", c.baseURL)
		if searchTerm != "" {
			// For projects, we'll need to do client-side filtering, so don't add search term to URL
		}
	default:
		return 0, fmt.Errorf("unknown resource type: %s", resourceType)
	}

	if !allItems {
		thirtyDaysAgo := time.Now().AddDate(0, 0, -30).Format(time.RFC3339)
		url = fmt.Sprintf("%s&last_activity_after=%s", url, thirtyDaysAgo)
	}

	var emptySlice []interface{} // Just need something to unmarshal into
	paginationInfo, err := c.get(url, &emptySlice)
	if err != nil {
		return 0, err
	}

	return paginationInfo.Total, nil
}

// GetProjects fetches projects, optionally filtered by search term and activity.
// Now checks resource count first if using allProjects flag.
func (c *Client) GetProjects(searchTerm string, allProjects bool) ([]Project, error) {
	// Check total count if we're using allProjects flag
	if allProjects {
		totalCount, err := c.CheckResourceCount("projects", allProjects, searchTerm)
		if err != nil {
			// Log the warning but proceed cautiously, as we don't know the real count
			c.logger.Printf("Warning: Could not determine project count: %v. Proceeding without confirmation.", err)
		} else {
			// Use the new confirmation function
			if !c.confirmLargeFetch("projects", totalCount) {
				// Return a specific error for cancellation
				return nil, fmt.Errorf("operation cancelled by user")
			}
		}
	}

	page := 1
	allProjectsList := []Project{}

	for {
		url := fmt.Sprintf("%s/api/v4/projects?per_page=100&order_by=last_activity_at&sort=desc&page=%d", c.baseURL, page)
		if !allProjects {
			thirtyDaysAgo := time.Now().AddDate(0, 0, -30).Format(time.RFC3339)
			url = fmt.Sprintf("%s&last_activity_after=%s", url, thirtyDaysAgo)
		}

		var projects []Project
		_, err := c.get(url, &projects)
		if err != nil {
			return nil, err // Error already includes context from c.get
		}

		c.logger.Printf("Received %d projects for page %d", len(projects), page)
		if len(projects) == 0 {
			break
		}
		allProjectsList = append(allProjectsList, projects...)
		page++
	}

	// Filter projects by search term (client-side)
	if searchTerm != "" {
		var filteredProjects []Project
		lowerSearchTerm := strings.ToLower(searchTerm)
		for _, project := range allProjectsList {
			if strings.Contains(strings.ToLower(project.PathWithNamespace), lowerSearchTerm) {
				filteredProjects = append(filteredProjects, project)
			}
		}
		c.logger.Printf("Filtered down to %d projects matching search term: %s", len(filteredProjects), searchTerm)
		return filteredProjects, nil
	}

	return allProjectsList, nil
}

// GetGroups fetches groups, optionally filtered by search term and activity.
// Now checks resource count first if using allGroups flag.
func (c *Client) GetGroups(searchTerm string, allGroups bool) ([]Group, error) {
	apiSearchUsed := searchTerm != ""

	// Check total count if we're using allGroups flag
	if allGroups {
		totalCount, err := c.CheckResourceCount("groups", allGroups, searchTerm)
		if err != nil {
			// Log the warning but proceed cautiously
			c.logger.Printf("Warning: Could not determine group count: %v. Proceeding without confirmation.", err)
		} else {
			// Use the new confirmation function
			resourceDesc := "groups"
			if searchTerm != "" {
				resourceDesc = fmt.Sprintf("groups matching '%s'", searchTerm) // More specific description
			}
			if !c.confirmLargeFetch(resourceDesc, totalCount) {
				// Return specific error for cancellation
				return nil, fmt.Errorf("operation cancelled by user")
			}
		}
	}

	page := 1
	allGroupsList := []Group{}

	for {
		url := fmt.Sprintf("%s/api/v4/groups?per_page=100&page=%d&all_available=true", c.baseURL, page)
		if !allGroups {
			thirtyDaysAgo := time.Now().AddDate(0, 0, -30).Format(time.RFC3339)
			url = fmt.Sprintf("%s&last_activity_after=%s", url, thirtyDaysAgo)
		}
		if apiSearchUsed {
			url = fmt.Sprintf("%s&search=%s", url, searchTerm)
		}

		var groups []Group
		_, err := c.get(url, &groups)
		if err != nil {
			return nil, err
		}

		c.logger.Printf("Received %d groups for page %d", len(groups), page)
		if len(groups) == 0 {
			break
		}
		allGroupsList = append(allGroupsList, groups...)
		page++
	}

	// Fallback manual filtering if API search was used but returned nothing
	if apiSearchUsed && len(allGroupsList) == 0 {
		c.logger.Printf("No groups found with API search for '%s', trying manual filtering", searchTerm)
		// Fetch all groups (respecting 'allGroups' flag) without the search term
		// Note: The recursive call here will re-trigger the confirmation check if needed.
		allGroupsNoSearch, err := c.GetGroups("", allGroups) // Recursive call
		if err != nil {
			// Check if the error was cancellation from the recursive call
			// Propagate the specific cancellation error if it occurred
			if err.Error() == "operation cancelled by user" {
				return nil, err
			}
			return nil, fmt.Errorf("error fetching groups for manual filtering: %w", err)
		}

		var filteredGroups []Group
		lowerSearchTerm := strings.ToLower(searchTerm)
		for _, group := range allGroupsNoSearch {
			if strings.Contains(strings.ToLower(group.FullPath), lowerSearchTerm) {
				filteredGroups = append(filteredGroups, group)
			}
		}
		c.logger.Printf("Manually filtered to %d groups containing '%s'", len(filteredGroups), searchTerm)
		return filteredGroups, nil
	}

	return allGroupsList, nil
}

// getSubgroups fetches direct subgroups for a given group ID.
// Now also checks resource count first if using allGroups flag.
func (c *Client) getSubgroups(groupID int, allGroups bool) ([]Group, error) {
	// First check how many subgroups there are
	url := fmt.Sprintf("%s/api/v4/groups/%d/subgroups?per_page=1&page=1", c.baseURL, groupID)
	if !allGroups {
		thirtyDaysAgo := time.Now().AddDate(0, 0, -30).Format(time.RFC3339)
		url = fmt.Sprintf("%s&last_activity_after=%s", url, thirtyDaysAgo)
	}

	var singleGroup []Group
	paginationInfo, err := c.get(url, &singleGroup)
	if err != nil {
		// Log warning, proceed without confirmation
		c.logger.Printf("Warning: Could not determine subgroup count for group %d: %v. Proceeding without confirmation.", groupID, err)
	} else if allGroups { // Only ask confirmation if fetching all items
		// Use the new confirmation function with context
		resourceDesc := fmt.Sprintf("subgroups for group %d", groupID)
		if !c.confirmLargeFetch(resourceDesc, paginationInfo.Total) {
			// Return specific error for cancellation
			return nil, fmt.Errorf("operation cancelled by user")
		}
	}

	page := 1
	subgroupsList := []Group{}

	for {
		url := fmt.Sprintf("%s/api/v4/groups/%d/subgroups?per_page=100&page=%d", c.baseURL, groupID, page)
		if !allGroups {
			thirtyDaysAgo := time.Now().AddDate(0, 0, -30).Format(time.RFC3339)
			url = fmt.Sprintf("%s&last_activity_after=%s", url, thirtyDaysAgo)
		}

		var groups []Group
		_, err := c.get(url, &groups)
		if err != nil {
			return nil, fmt.Errorf("error fetching subgroups for group %d: %w", groupID, err)
		}

		c.logger.Printf("Received %d subgroups for group ID %d, page %d", len(groups), groupID, page)
		if len(groups) == 0 {
			break
		}
		subgroupsList = append(subgroupsList, groups...)
		page++
	}
	return subgroupsList, nil
}

// getProjectsForGroup fetches direct projects for a given group ID.
// Now also checks resource count first if using allProjects flag.
func (c *Client) getProjectsForGroup(groupID int, allProjects bool) ([]Project, error) {
	// First check how many projects there are
	url := fmt.Sprintf("%s/api/v4/groups/%d/projects?per_page=1&page=1&include_subgroups=false", c.baseURL, groupID)
	if !allProjects {
		thirtyDaysAgo := time.Now().AddDate(0, 0, -30).Format(time.RFC3339)
		url = fmt.Sprintf("%s&last_activity_after=%s", url, thirtyDaysAgo)
	}

	var singleProject []Project
	paginationInfo, err := c.get(url, &singleProject)
	if err != nil {
		// Log warning, proceed without confirmation
		c.logger.Printf("Warning: Could not determine project count for group %d: %v. Proceeding without confirmation.", groupID, err)
	} else if allProjects { // Only ask confirmation if fetching all items
		// Use the new confirmation function with context
		resourceDesc := fmt.Sprintf("projects for group %d", groupID)
		if !c.confirmLargeFetch(resourceDesc, paginationInfo.Total) {
			// Return specific error for cancellation
			return nil, fmt.Errorf("operation cancelled by user")
		}
	}

	page := 1
	projectsList := []Project{}

	for {
		url := fmt.Sprintf("%s/api/v4/groups/%d/projects?per_page=100&page=%d&include_subgroups=false", c.baseURL, groupID, page)
		if !allProjects {
			thirtyDaysAgo := time.Now().AddDate(0, 0, -30).Format(time.RFC3339)
			url = fmt.Sprintf("%s&last_activity_after=%s", url, thirtyDaysAgo)
		}

		var projects []Project
		_, err := c.get(url, &projects)
		if err != nil {
			return nil, fmt.Errorf("error fetching projects for group %d: %w", groupID, err)
		}

		c.logger.Printf("Received %d projects for group ID %d, page %d", len(projects), groupID, page)
		if len(projects) == 0 {
			break
		}
		projectsList = append(projectsList, projects...)
		page++
	}
	return projectsList, nil
}

// PopulateGroupHierarchy recursively fetches projects and subgroups for a given group.
// It modifies the passed group pointer and handles cancellation errors.
func (c *Client) PopulateGroupHierarchy(group *Group, allItems bool) error {
	c.logger.Printf("Populating hierarchy for group: %s (ID: %d)", group.FullPath, group.ID)
	var firstError error // Keep track of the first error (especially cancellation)

	// Get projects for the current group
	projects, err := c.getProjectsForGroup(group.ID, allItems)
	if err != nil {
		// Check for cancellation first
		if err.Error() == "operation cancelled by user" {
			return err // Propagate cancellation immediately
		}
		// Log other errors but continue, maybe we can still get subgroups
		c.logger.Printf("Error getting projects for group %d: %v", group.ID, err)
		if firstError == nil {
			firstError = fmt.Errorf("failed getting projects for group %d: %w", group.ID, err)
		}
	} else {
		group.Projects = projects
		c.logger.Printf("Found %d projects for group %d", len(projects), group.ID)
	}

	// Get direct subgroups for the current group
	subgroups, err := c.getSubgroups(group.ID, allItems)
	if err != nil {
		// Check for cancellation first
		if err.Error() == "operation cancelled by user" {
			return err // Propagate cancellation immediately
		}
		// Log other errors but continue, maybe we already got projects
		c.logger.Printf("Error getting subgroups for group %d: %v", group.ID, err)
		if firstError == nil {
			firstError = fmt.Errorf("failed getting subgroups for group %d: %w", group.ID, err)
		}
		// Return the recorded error (if any) or nil if we successfully got projects earlier
		return firstError // Be lenient only if no error recorded yet
	}
	c.logger.Printf("Found %d direct subgroups for group %d", len(subgroups), group.ID)

	// Recursively populate each subgroup
	group.Subgroups = make([]Group, len(subgroups)) // Allocate space
	for i := range subgroups {
		currentSubgroup := subgroups[i]                             // Make a copy
		err := c.PopulateGroupHierarchy(&currentSubgroup, allItems) // Recursive call
		if err != nil {
			// Check for cancellation first
			if err.Error() == "operation cancelled by user" {
				return err // Propagate cancellation immediately
			}
			// Log error for this specific subgroup but continue with others
			c.logger.Printf("Error populating hierarchy for subgroup %d (%s): %v", currentSubgroup.ID, currentSubgroup.Name, err)
			if firstError == nil {
				firstError = fmt.Errorf("failed to populate subgroup %s: %w", currentSubgroup.Name, err) // Record first population error
			}
			// Continue processing other subgroups even if one fails
		}
		group.Subgroups[i] = currentSubgroup // Assign the populated subgroup back
	}

	// Sort children alphabetically (subgroups first, then projects)
	sort.SliceStable(group.Subgroups, func(i, j int) bool {
		return strings.ToLower(group.Subgroups[i].Name) < strings.ToLower(group.Subgroups[j].Name)
	})
	sort.SliceStable(group.Projects, func(i, j int) bool {
		return strings.ToLower(group.Projects[i].Name) < strings.ToLower(group.Projects[j].Name)
	})

	return firstError // Return the first error encountered during population (or nil)
}
