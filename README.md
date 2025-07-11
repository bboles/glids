# glids - GitLab ID Lister

`glids` is a small command-line utility for quickly listing GitLab Group and Project IDs based on search terms and activity. It can display projects, groups, or a full group/project hierarchy.

When working with the GitLab API, you have to plug in a numeric ID instead of the group or project name in many places.  This can be painful if there are dozens or hundreds of groups/projects you are working with.  I made this tool so I could quickly map the group/project names to their respective ID.

## Features

*   List projects and groups matching a search term (default mode).
*   List only groups matching a search term (`--groups`).
*   List only projects matching a search term (`--projects`).
*   Display a hierarchical view of groups, subgroups, and their projects (`--hierarchy`).
*   Filter results by recent activity (last 30 days by default).
*   Option to show all items regardless of activity (`--all`).
*   Configure GitLab host via `--host` flag or `GITLAB_HOST` environment variable.
*   Requires a GitLab Personal Access Token via `GITLAB_TOKEN` environment variable.
*   Debug logging (`--debug`).
*   Can be installed via Homebrew.
*   No third-party modules used.

## Demo

![](./vhs/glids_group_hierarchy.gif)

## Installation

### Using Homebrew/Linuxbrew

```bash
brew tap bboles/tap
brew install glids
```

### Using `go install`

```bash
go install github.com/bboles/glids/cmd/glids@latest
```

### From Source

1.  Clone the repository:
    ```bash
    git clone https://github.com/bboles/glids.git
    cd glids
    ```
2.  Build the binary:
    ```bash
    go build -o glids ./cmd/glids
    ```
3.  (Optional) Move the `glids` binary to a directory in your `$PATH`.

## Configuration

1.  **GitLab Host:**
    *   Set the `GITLAB_HOST` environment variable:
        ```bash
        export GITLAB_HOST="gitlab.example.com"
        ```
    *   *Or*, use the `--host` flag when running the command:
        ```bash
        glids --host gitlab.example.com <search_term>
        ```
    *   The `--host` flag takes precedence over the environment variable.

2.  **GitLab Token:**
    *   Set the `GITLAB_TOKEN` environment variable with a Personal Access Token (PAT) that has `api` or `read_api` scope:
        ```bash
        export GITLAB_TOKEN="your_gitlab_api_token"
        ```

## Usage

```bash
glids [flags] [search_term]
```

*   `search_term`: (Optional) A term to filter projects or groups by name/path. If omitted, lists recently active items. Can also be provided via `--search`.

### Flags

*   `--search <term>`: Explicitly provide the search term.  The string is passed to either the [group search](https://docs.gitlab.com/api/search/#group-search-api) or [project search](https://docs.gitlab.com/api/search/#project-search-api) API (depending on the other flags used).
*   `--groups`: List groups only.
*   `--projects`: List projects only.
*   `--hierarchy`: Show a hierarchical tree view starting from matching groups.
*   `--all`: Include all projects/groups, ignoring the default 30-day activity filter.
*   `--host <host>`: Specify the GitLab server hostname (e.g., `gitlab.com`). Overrides `GITLAB_HOST`.
*   `--debug`: Enable verbose debug logging to stderr.
*   `--nohttps`: Disable HTTPS and use HTTP for API calls.
*   `--help`: Show help message.

### Examples

1.  **List recently active projects and groups matching "my-app":**
    ```bash
    glids my-app
    # or
    glids --search my-app
    ```

2.  **List *all* projects matching "my-app" (including inactive):**
    ```bash
    glids --projects --all my-app
    ```

3.  **List recently active groups matching "platform":**
    ```bash
    glids --groups platform
    ```

4.  **Show the hierarchy for the group "platform/teams":**
    ```bash
    glids --hierarchy platform/teams
    ```

5.  **Disable HTTPS (use HTTP) for API calls:**
    ```bash
    glids --nohttps platform
    # or 
    GLID_NOHTTPS=true glids platform
    ```

6.  **List all recently active projects and groups on a specific GitLab instance:**
    ```bash
    glids --host gitlab.mycompany.com
    ```

7.  **List groups with debug output:**
    ```bash
    glids --groups --debug internal-tools
    ```
