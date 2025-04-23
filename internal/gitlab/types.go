package gitlab

// Project represents a GitLab project.
type Project struct {
	ID                int    `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
	Name              string `json:"name"`
}

// Group represents a GitLab group or subgroup.
type Group struct {
	ID        int       `json:"id"`
	ParentID  *int      `json:"parent_id"`
	FullPath  string    `json:"full_path"`
	Name      string    `json:"name"`
	Subgroups []Group   `json:"-"` // Populated manually
	Projects  []Project `json:"-"` // Populated manually
}

// PaginationInfo holds information about the total resources and pagination.
type PaginationInfo struct {
	Total       int
	PerPage     int
	TotalPages  int
	CurrentPage int
}
