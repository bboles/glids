package gitlab

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Client handles communication with the GitLab API.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
	logger     *log.Logger
}

// NewClient creates a new GitLab API client.
func NewClient(baseURL, token string, logger *log.Logger) *Client {
	if logger == nil {
		logger = log.New(io.Discard, "", 0) // Default to discarding logs if none provided
	}
	return &Client{
		baseURL:    baseURL,
		token:      token,
		httpClient: &http.Client{},
		logger:     logger,
	}
}

// Helper function for making authenticated GET requests and decoding JSON
func (c *Client) get(url string, target interface{}) error {
	c.logger.Printf("Making API request to: %s", url)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("error creating request: %v", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("error making API request: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("error reading response body: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		c.logger.Printf("API request failed with status %d: %s", resp.StatusCode, body)
		return fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, body)
	}

	err = json.Unmarshal(body, target)
	if err != nil {
		c.logger.Printf("Error parsing JSON response: %v, response body: %s", err, string(body))
		return fmt.Errorf("error parsing JSON response: %v", err)
	}
	return nil
}

// GetProjects fetches projects, optionally filtered by search term and activity.
func (c *Client) GetProjects(searchTerm string, allProjects bool) ([]Project, error) {
	page := 1
	allProjectsList := []Project{}

	for {
		url := fmt.Sprintf("%s/api/v4/projects?per_page=100&order_by=last_activity_at&sort=desc&page=%d", c.baseURL, page)
		if !allProjects {
			thirtyDaysAgo := time.Now().AddDate(0, 0, -30).Format(time.RFC3339)
			url = fmt.Sprintf("%s&last_activity_after=%s", url, thirtyDaysAgo)
		}

		var projects []Project
		err := c.get(url, &projects)
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
// Includes fallback to manual filtering if API search yields no results.
func (c *Client) GetGroups(searchTerm string, allGroups bool) ([]Group, error) {
	page := 1
	allGroupsList := []Group{}
	apiSearchUsed := searchTerm != ""

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
		err := c.get(url, &groups)
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
		allGroupsNoSearch, err := c.GetGroups("", allGroups) // Recursive call without search
		if err != nil {
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
func (c *Client) getSubgroups(groupID int, allGroups bool) ([]Group, error) {
	page := 1
	subgroupsList := []Group{}

	for {
		url := fmt.Sprintf("%s/api/v4/groups/%d/subgroups?per_page=100&page=%d", c.baseURL, groupID, page)
		if !allGroups {
			thirtyDaysAgo := time.Now().AddDate(0, 0, -30).Format(time.RFC3339)
			url = fmt.Sprintf("%s&last_activity_after=%s", url, thirtyDaysAgo)
		}

		var groups []Group
		err := c.get(url, &groups)
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
func (c *Client) getProjectsForGroup(groupID int, allProjects bool) ([]Project, error) {
	page := 1
	projectsList := []Project{}

	for {
		url := fmt.Sprintf("%s/api/v4/groups/%d/projects?per_page=100&page=%d&include_subgroups=false", c.baseURL, groupID, page)
		if !allProjects {
			thirtyDaysAgo := time.Now().AddDate(0, 0, -30).Format(time.RFC3339)
			url = fmt.Sprintf("%s&last_activity_after=%s", url, thirtyDaysAgo)
		}

		var projects []Project
		err := c.get(url, &projects)
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
// It modifies the passed group pointer.
func (c *Client) PopulateGroupHierarchy(group *Group, allItems bool) error {
	c.logger.Printf("Populating hierarchy for group: %s (ID: %d)", group.FullPath, group.ID)

	// Get projects for the current group
	projects, err := c.getProjectsForGroup(group.ID, allItems)
	if err != nil {
		// Log error but continue, maybe we can still get subgroups
		c.logger.Printf("Error getting projects for group %d: %v", group.ID, err)
		// Don't return the error, allow subgroup fetching
	} else {
		group.Projects = projects
		c.logger.Printf("Found %d projects for group %d", len(projects), group.ID)
	}

	// Get direct subgroups for the current group
	subgroups, err := c.getSubgroups(group.ID, allItems)
	if err != nil {
		// Log error but continue, maybe we already got projects
		c.logger.Printf("Error getting subgroups for group %d: %v", group.ID, err)
		// Return nil because we might have successfully fetched projects
		return nil // Be lenient
	}
	c.logger.Printf("Found %d direct subgroups for group %d", len(subgroups), group.ID)

	// Recursively populate each subgroup
	group.Subgroups = make([]Group, len(subgroups)) // Allocate space
	for i := range subgroups {
		currentSubgroup := subgroups[i] // Make a copy to avoid issues with loop variable address
		err := c.PopulateGroupHierarchy(&currentSubgroup, allItems) // Recursive call
		if err != nil {
			// Log error for this specific subgroup but continue with others
			c.logger.Printf("Error populating hierarchy for subgroup %d (%s): %v", currentSubgroup.ID, currentSubgroup.Name, err)
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

	return nil // Overall success for this level, even if some children had issues
}

// Note: getGroupByPath was removed as it wasn't used in the main logic flow provided.
// If needed, it can be added back as a method on the Client.
