package display

import (
	"fmt"
	"os"
	"text/tabwriter"

	"glids/internal/gitlab"
)

const (
	treeBranch     = "├"
	treeCorner     = "└"
	treeVertical   = "│"
	treeHorizontal = "─"
	treeSpace      = " "
)

// PrintProjectList prints a list of projects using tabwriter.
// nameWidth is the desired width for the project path column. If 0, tabwriter auto-sizes.
func PrintProjectList(projects []gitlab.Project, nameWidth int) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0) // Changed AlignRight to 0
	defer w.Flush()
	for _, project := range projects {
		displayName := project.PathWithNamespace + ":"
		formattedName := displayName
		if nameWidth > 0 {
			formattedName = fmt.Sprintf("%-*s", nameWidth, displayName)
		}
		fmt.Fprintf(w, "%s\t%6d\n", formattedName, project.ID)
	}
}

// PrintGroupList prints a list of groups using tabwriter.
// nameWidth is the desired width for the group path column. If 0, tabwriter auto-sizes.
func PrintGroupList(groups []gitlab.Group, nameWidth int) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0) // Changed AlignRight to 0
	defer w.Flush()
	for _, group := range groups {
		displayName := group.FullPath + ":"
		formattedName := displayName
		if nameWidth > 0 {
			formattedName = fmt.Sprintf("%-*s", nameWidth, displayName)
		}
		fmt.Fprintf(w, "%s\t%6d\n", formattedName, group.ID)
	}
}

// PrintHierarchy prints the full hierarchy starting from a root group.
func PrintHierarchy(rootGroup gitlab.Group) {
	fmt.Printf("\n%s (ID: %d)\n", rootGroup.FullPath, rootGroup.ID) // Print the root group path itself

	totalChildren := len(rootGroup.Subgroups) + len(rootGroup.Projects)
	childIndex := 0

	// Print subgroups (already sorted by PopulateGroupHierarchy)
	for _, subgroup := range rootGroup.Subgroups {
		childIndex++
		printHierarchyRecursive(subgroup, "", childIndex == totalChildren) // Start with empty prefix
	}

	// Print projects (already sorted by PopulateGroupHierarchy)
	for _, project := range rootGroup.Projects {
		childIndex++
		printHierarchyRecursive(project, "", childIndex == totalChildren) // Start with empty prefix
	}
}

// printHierarchyRecursive is the internal recursive helper for PrintHierarchy.
func printHierarchyRecursive(item interface{}, prefix string, isLast bool) {
	connector := treeBranch
	if isLast {
		connector = treeCorner
	}

	switch v := item.(type) {
	case gitlab.Group:
		// Print the group node
		fmt.Printf("%s%s%s%s %s [G] [ID=%d]\n", prefix, connector, treeHorizontal, treeSpace, v.Name, v.ID)

		// Prepare prefix for children
		childPrefix := prefix
		if isLast {
			childPrefix += treeSpace + treeSpace + treeSpace + treeSpace // 4 spaces
		} else {
			childPrefix += treeVertical + treeSpace + treeSpace + treeSpace // Vertical line + 3 spaces
		}

		// Print subgroups and projects
		totalChildren := len(v.Subgroups) + len(v.Projects)
		childIndex := 0

		for _, subgroup := range v.Subgroups {
			childIndex++
			printHierarchyRecursive(subgroup, childPrefix, childIndex == totalChildren)
		}
		for _, project := range v.Projects {
			childIndex++
			printHierarchyRecursive(project, childPrefix, childIndex == totalChildren)
		}

	case gitlab.Project:
		// Print the project node (leaf)
		fmt.Printf("%s%s%s%s %s [P] [ID=%d]\n", prefix, connector, treeHorizontal, treeSpace, v.Name, v.ID)
	}
}
