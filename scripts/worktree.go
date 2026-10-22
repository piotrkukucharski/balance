package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func runCmdInDir(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func cmdCombinedOutput(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func cmdCombinedOutputInDir(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func cmdOutput(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func cmdOutputInDir(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func getTopLevel() (string, error) {
	return cmdOutput("git", "rev-parse", "--show-toplevel")
}

func isMainTree(topLevel string) bool {
	gitPath := filepath.Join(topLevel, ".git")
	fi, err := os.Stat(gitPath)
	if err != nil {
		return false
	}
	return fi.IsDir()
}

func isPortInUse(port int) bool {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		return true
	}
	// Also test 0.0.0.0 or listen check
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return true
	}
	_ = ln.Close()
	return false
}

func findFreePort() (int, error) {
	startPort := 10000
	endPort := 11000
	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	for i := 0; i < 50; i++ {
		p := startPort + r.Intn(endPort-startPort+1)
		if !isPortInUse(p) {
			return p, nil
		}
	}

	for p := startPort; p <= endPort; p++ {
		if !isPortInUse(p) {
			return p, nil
		}
	}

	return 0, fmt.Errorf("no free port found in range %d-%d", startPort, endPort)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target)
	})
}

func backupSQLiteDBs(srcDataDir, dstDataDir string) error {
	sqlite3Path, err := exec.LookPath("sqlite3")
	useSqlite3 := (err == nil && sqlite3Path != "")

	return filepath.Walk(srcDataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(info.Name(), "-shm") {
			return nil
		}
		rel, err := filepath.Rel(srcDataDir, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dstDataDir, rel)

		if strings.HasSuffix(info.Name(), ".db") && useSqlite3 {
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			_ = os.Remove(target)
			_ = os.Remove(target + "-wal")
			_ = os.Remove(target + "-shm")
			backupCmd := fmt.Sprintf(".backup '%s'", target)
			cmd := exec.Command("sqlite3", path, backupCmd)
			if out, bErr := cmd.CombinedOutput(); bErr != nil {
				return fmt.Errorf("sqlite3 backup error for %s: %v (%s)", path, bErr, string(out))
			}
			return nil
		}

		if strings.HasSuffix(info.Name(), ".db") && !useSqlite3 {
			// Without sqlite3, copy .db directly
			return copyFile(path, target)
		}

		if strings.HasSuffix(info.Name(), "-wal") && !useSqlite3 {
			return copyFile(path, target)
		}

		return copyFile(path, target)
	})
}

// transliteratePolish converts common Polish diacritics to ASCII equivalents.
func transliteratePolish(s string) string {
	replacer := strings.NewReplacer(
		"ą", "a", "ć", "c", "ę", "e", "ł", "l", "ń", "n", "ó", "o", "ś", "s", "ź", "z", "ż", "z",
		"Ą", "a", "Ć", "c", "Ę", "e", "Ł", "l", "Ń", "n", "Ó", "o", "Ś", "s", "Ź", "z", "Ż", "z",
	)
	return replacer.Replace(s)
}

// slugify converts a human-readable title into a URL/git-friendly slug.
func slugify(s string) string {
	s = transliteratePolish(s)
	s = strings.ToLower(s)

	var sb strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			sb.WriteRune(r)
		} else if r == ' ' || r == '-' || r == '_' || r == '/' || r == '.' {
			sb.WriteRune('-')
		}
	}
	res := sb.String()
	// Collapse multiple hyphens
	re := regexp.MustCompile(`-+`)
	res = re.ReplaceAllString(res, "-")
	res = strings.Trim(res, "-")
	if len(res) > 50 {
		res = res[:50]
		res = strings.TrimRight(res, "-")
	}
	return res
}

type vikunjaTaskResponse struct {
	ID         int64  `json:"id"`
	Index      int64  `json:"index"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
}

func readEnvVarFromFiles(topLevel, key string) string {
	// First check process environment
	if val := os.Getenv(key); strings.TrimSpace(val) != "" {
		return strings.TrimSpace(val)
	}

	for _, name := range []string{".env", ".env.local"} {
		p := filepath.Join(topLevel, name)
		if content, err := os.ReadFile(p); err == nil {
			for _, line := range strings.Split(string(content), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, key+"=") {
					val := strings.TrimPrefix(line, key+"=")
					val = strings.Trim(val, `"' `)
					if val != "" {
						return val
					}
				}
			}
		}
	}
	return ""
}

// resolveWorktreeName attempts to resolve input (e.g. BALANCE-2, 80, 2) to a task title via Vikunja API,
// returning formatted "balance-2--slug-title". If not a task ID or resolution fails, returns original input.
func resolveWorktreeName(topLevel, input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return input
	}

	apiURL := readEnvVarFromFiles(topLevel, "VIKUNJA_API_URL")
	apiToken := readEnvVarFromFiles(topLevel, "VIKUNJA_API_TOKEN")
	projectIDStr := readEnvVarFromFiles(topLevel, "VIKUNJA_PROJECT_ID")
	if projectIDStr == "" {
		projectIDStr = "29"
	}

	if apiURL == "" || apiToken == "" {
		return input
	}
	apiURL = strings.TrimRight(apiURL, "/")

	// Check if input matches task patterns like "BALANCE-2", "balance-2", "2", or numeric ID
	taskRe := regexp.MustCompile(`(?i)^(?:BALANCE-)?(\d+)$`)
	matches := taskRe.FindStringSubmatch(input)
	if len(matches) < 2 {
		return input
	}
	indexOrID := matches[1]

	client := &http.Client{Timeout: 5 * time.Second}

	// 1. Try by-index in project: /projects/{projectID}/tasks/by-index/{index}
	byIndexURL := fmt.Sprintf("%s/projects/%s/tasks/by-index/%s", apiURL, projectIDStr, indexOrID)
	req, err := http.NewRequest(http.MethodGet, byIndexURL, nil)
	if err == nil {
		req.Header.Set("Authorization", "Bearer "+apiToken)
		req.Header.Set("Accept", "application/json")
		if resp, err := client.Do(req); err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var task vikunjaTaskResponse
				if err := json.NewDecoder(resp.Body).Decode(&task); err == nil && task.Title != "" {
					slug := slugify(task.Title)
					return fmt.Sprintf("balance-%s--%s", indexOrID, slug)
				}
			}
		}
	}

	// 2. Try direct task ID: /tasks/{id}
	byTaskIDURL := fmt.Sprintf("%s/tasks/%s", apiURL, indexOrID)
	req, err = http.NewRequest(http.MethodGet, byTaskIDURL, nil)
	if err == nil {
		req.Header.Set("Authorization", "Bearer "+apiToken)
		req.Header.Set("Accept", "application/json")
		if resp, err := client.Do(req); err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var task vikunjaTaskResponse
				if err := json.NewDecoder(resp.Body).Decode(&task); err == nil && task.Title != "" {
					slug := slugify(task.Title)
					taskIdx := fmt.Sprintf("%d", task.Index)
					if task.Index == 0 {
						taskIdx = indexOrID
					}
					return fmt.Sprintf("balance-%s--%s", taskIdx, slug)
				}
			}
		}
	}

	return input
}

func cmdCreate(args []string) error {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return fmt.Errorf("worktree name or task ID is required. Usage: task dev:worktree:create -- <task-id-or-name>")
	}
	input := strings.TrimSpace(args[0])

	topLevel, err := getTopLevel()
	if err != nil {
		return fmt.Errorf("failed to get repository root: %w", err)
	}

	name := resolveWorktreeName(topLevel, input)
	if name != input {
		fmt.Printf("Resolved task %s -> worktree name '%s'\n", input, name)
	}

	targetDir := filepath.Clean(filepath.Join(topLevel, "..", "development", name))
	if fi, err := os.Stat(targetDir); err == nil && fi.IsDir() {
		return fmt.Errorf("directory %s already exists", targetDir)
	}

	if err := os.MkdirAll(filepath.Dir(targetDir), 0755); err != nil {
		return fmt.Errorf("failed creating parent directory: %w", err)
	}

	// Check if branch exists
	branchExists := false
	if err := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+name).Run(); err == nil {
		branchExists = true
	}

	if branchExists {
		fmt.Printf("Adding worktree for existing branch '%s'...\n", name)
		if err := runCmd("git", "worktree", "add", targetDir, name); err != nil {
			return fmt.Errorf("git worktree add failed: %w", err)
		}
	} else {
		fmt.Printf("Creating branch '%s' and adding worktree...\n", name)
		if err := runCmd("git", "worktree", "add", "-b", name, targetDir, "HEAD"); err != nil {
			return fmt.Errorf("git worktree add -b failed: %w", err)
		}
	}

	freePort, err := findFreePort()
	if err != nil {
		return err
	}

	sourceEnv := filepath.Join(topLevel, ".env")
	if _, err := os.Stat(sourceEnv); os.IsNotExist(err) {
		sourceEnvExample := filepath.Join(topLevel, ".env.example")
		if _, err := os.Stat(sourceEnvExample); err == nil {
			sourceEnv = sourceEnvExample
		}
	}

	destEnv := filepath.Join(targetDir, ".env")
	if _, err := os.Stat(sourceEnv); err == nil {
		content, err := os.ReadFile(sourceEnv)
		if err != nil {
			return fmt.Errorf("reading source env: %w", err)
		}
		lines := strings.Split(string(content), "\n")
		foundPort := false
		for i, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), "PORT=") {
				lines[i] = fmt.Sprintf("PORT=%d", freePort)
				foundPort = true
				break
			}
		}
		if !foundPort {
			lines = append(lines, fmt.Sprintf("PORT=%d", freePort))
		}
		if err := os.WriteFile(destEnv, []byte(strings.Join(lines, "\n")), 0644); err != nil {
			return fmt.Errorf("writing dest env: %w", err)
		}
	} else {
		envContent := fmt.Sprintf("PORT=%d\nDATA_DIR=./data\n", freePort)
		if err := os.WriteFile(destEnv, []byte(envContent), 0644); err != nil {
			return fmt.Errorf("writing dest env: %w", err)
		}
	}

	// Copy data directory
	dataDirRel := "./data"
	if content, err := os.ReadFile(destEnv); err == nil {
		for _, line := range strings.Split(string(content), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "DATA_DIR=") {
				val := strings.TrimPrefix(line, "DATA_DIR=")
				val = strings.Trim(val, `"' `)
				if val != "" {
					dataDirRel = val
				}
				break
			}
		}
	}

	cleanRel := strings.TrimPrefix(dataDirRel, "./")
	absSourceData := filepath.Join(topLevel, cleanRel)
	absDestData := filepath.Join(targetDir, cleanRel)

	if fi, err := os.Stat(absSourceData); err == nil && fi.IsDir() {
		fmt.Printf("Copying data directory from %s to %s...\n", absSourceData, absDestData)
		_ = os.MkdirAll(filepath.Join(absDestData, "budgets"), 0755)
		if err := backupSQLiteDBs(absSourceData, absDestData); err != nil {
			fmt.Printf("Warning: failed backing up sqlite dbs: %v\n", err)
		}
	} else {
		_ = os.MkdirAll(filepath.Join(absDestData, "budgets"), 0755)
	}

	// Copy .agents and skills-lock.json
	agentsSrc := filepath.Join(topLevel, ".agents")
	if fi, err := os.Stat(agentsSrc); err == nil && fi.IsDir() {
		fmt.Printf("Copying .agents directory to %s...\n", filepath.Join(targetDir, ".agents"))
		_ = copyDir(agentsSrc, filepath.Join(targetDir, ".agents"))
	}

	skillsLockSrc := filepath.Join(topLevel, "skills-lock.json")
	if _, err := os.Stat(skillsLockSrc); err == nil {
		_ = copyFile(skillsLockSrc, filepath.Join(targetDir, "skills-lock.json"))
	}

	fmt.Printf("Worktree '%s' successfully created at %s!\n", name, targetDir)
	fmt.Printf("Allocated port: %d\n", freePort)
	fmt.Printf("To use: cd %s && task dev:up\n", targetDir)
	return nil
}

func readEnvPort(envPath string) (int, error) {
	content, err := os.ReadFile(envPath)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "PORT=") {
			val := strings.TrimPrefix(line, "PORT=")
			val = strings.Trim(val, `"' `)
			p, err := strconv.Atoi(val)
			if err != nil {
				return 0, err
			}
			return p, nil
		}
	}
	return 0, fmt.Errorf("PORT variable not found in %s", envPath)
}

func cmdClear(args []string) error {
	var name string
	deleteBranch := false
	force := false

	for _, arg := range args {
		switch arg {
		case "--delete-branch", "DELETE_BRANCH=true", "true":
			deleteBranch = true
		case "--force":
			force = true
		default:
			if !strings.HasPrefix(arg, "-") && name == "" {
				name = arg
			}
		}
	}

	if name == "" {
		return fmt.Errorf("worktree name is required. Usage: task dev:worktree:clear -- <name> [--delete-branch] [--force]")
	}

	topLevel, err := getTopLevel()
	if err != nil {
		return fmt.Errorf("failed to get repository root: %w", err)
	}

	absTargetDir := filepath.Clean(filepath.Join(topLevel, "..", "development", name))

	// Check port in worktree .env
	envFile := filepath.Join(absTargetDir, ".env")
	if port, err := readEnvPort(envFile); err == nil {
		if isPortInUse(port) {
			if force {
				fmt.Fprintf(os.Stderr, "Warning: Port %d is currently in use! Proceeding anyway due to --force flag.\n", port)
			} else {
				return fmt.Errorf("server on port %d is still running for worktree '%s'. Please close the server before clearing, or run with --force", port, name)
			}
		}
	}

	wtList, _ := cmdOutput("git", "worktree", "list")
	dirExists := false
	if _, err := os.Stat(absTargetDir); err == nil {
		dirExists = true
	}

	if dirExists || strings.Contains(wtList, absTargetDir) {
		fmt.Printf("Removing worktree at %s...\n", absTargetDir)
		if err := runCmd("git", "worktree", "remove", "--force", absTargetDir); err != nil {
			_ = os.RemoveAll(absTargetDir)
		}
		_ = runCmd("git", "worktree", "prune")
	} else {
		fmt.Printf("Worktree at %s not found in git worktree list.\n", absTargetDir)
	}

	if deleteBranch {
		if err := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+name).Run(); err == nil {
			fmt.Printf("Deleting branch '%s'...\n", name)
			_ = runCmd("git", "branch", "-D", name)
		}
	} else {
		fmt.Printf("Branch '%s' preserved (pass --delete-branch to delete branch as well).\n", name)
	}

	fmt.Printf("Clear operation for '%s' completed.\n", name)
	return nil
}

func cmdUpdateMaintree(args []string) error {
	topLevel, err := getTopLevel()
	if err != nil {
		return fmt.Errorf("failed to get repository root: %w", err)
	}

	if !isMainTree(topLevel) {
		return fmt.Errorf("dev:worktree:update:maintree can only be run from the main repository tree")
	}

	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return fmt.Errorf("branch/worktree name is required. Usage: task dev:worktree:update:maintree -- <name>")
	}
	name := strings.TrimSpace(args[0])

	if err := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+name).Run(); err != nil {
		return fmt.Errorf("branch '%s' does not exist", name)
	}

	fmt.Printf("Fast-forwarding main tree with branch '%s'...\n", name)
	if err := runCmd("git", "merge", "--ff-only", name); err != nil {
		return fmt.Errorf("fast-forward failed: %w", err)
	}
	fmt.Printf("Main tree successfully updated from '%s'.\n", name)
	return nil
}

func cmdUpdateBranch(args []string) error {
	topLevel, err := getTopLevel()
	if err != nil {
		return fmt.Errorf("failed to get repository root: %w", err)
	}

	if isMainTree(topLevel) {
		return fmt.Errorf("dev:worktree:update:branch must be run from inside a secondary worktree, not maintree")
	}

	currentBranch, err := cmdOutput("git", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return fmt.Errorf("failed to get current branch: %w", err)
	}

	fmt.Printf("Rebasing worktree branch '%s' onto main...\n", currentBranch)
	if err := runCmd("git", "rebase", "main"); err != nil {
		return fmt.Errorf("git rebase main failed: %w", err)
	}
	fmt.Printf("Worktree branch '%s' successfully rebased on main.\n", currentBranch)
	return nil
}

type worktreeInfo struct {
	path   string
	branch string
	commit string
}

func getWorktreeList() ([]worktreeInfo, error) {
	out, err := cmdOutput("git", "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}

	var list []worktreeInfo
	var cur worktreeInfo
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "worktree ") {
			if cur.path != "" {
				list = append(list, cur)
			}
			cur = worktreeInfo{path: strings.TrimPrefix(line, "worktree ")}
		} else if strings.HasPrefix(line, "branch refs/heads/") {
			cur.branch = strings.TrimPrefix(line, "branch refs/heads/")
		} else if strings.HasPrefix(line, "HEAD ") {
			cur.commit = strings.TrimPrefix(line, "HEAD ")
		} else if line == "" {
			if cur.path != "" {
				list = append(list, cur)
				cur = worktreeInfo{}
			}
		}
	}
	if cur.path != "" {
		list = append(list, cur)
	}
	return list, nil
}

type silentError struct{}

func (e silentError) Error() string {
	return ""
}

func cmdFinish(args []string) error {
	topLevel, err := getTopLevel()
	if err != nil {
		return fmt.Errorf("failed to get repository root: %w", err)
	}

	// 1. Sprawdzenie czy skrypt jest uruchamiany z głównego drzewa
	if !isMainTree(topLevel) {
		return fmt.Errorf("dev:worktree:finish must be executed from the main tree repository")
	}

	force := false
	for _, arg := range args {
		if arg == "--force" {
			force = true
		}
	}

	// 2. Pobierz listę worktree
	allTrees, err := getWorktreeList()
	if err != nil {
		return fmt.Errorf("failed to retrieve worktree list: %w", err)
	}

	var secondaryTrees []worktreeInfo
	for _, wt := range allTrees {
		// Pomiń główne drzewo
		cleanWt := filepath.Clean(wt.path)
		cleanTop := filepath.Clean(topLevel)
		if cleanWt != cleanTop {
			secondaryTrees = append(secondaryTrees, wt)
		}
	}

	if len(secondaryTrees) == 0 {
		fmt.Println("No active secondary worktrees found.")
		return nil
	}

	// Wyświetl listę
	fmt.Println("Available worktrees:")
	for i, wt := range secondaryTrees {
		branchDisplay := wt.branch
		if branchDisplay == "" {
			branchDisplay = "(detached HEAD)"
		}
		fmt.Printf("[%d] %s (path: %s)\n", i+1, branchDisplay, wt.path)
	}

	// Wybór użytkownika
	var choiceIdx int
	for {
		fmt.Printf("Select worktree to finish [1-%d]: ", len(secondaryTrees))
		var input string
		if _, err := fmt.Scanln(&input); err != nil {
			return fmt.Errorf("reading selection failed: %w", err)
		}
		input = strings.TrimSpace(input)
		val, err := strconv.Atoi(input)
		if err == nil && val >= 1 && val <= len(secondaryTrees) {
			choiceIdx = val - 1
			break
		}
		fmt.Println("Invalid choice, please try again.")
	}

	selected := secondaryTrees[choiceIdx]
	worktreePath := selected.path
	branchName := selected.branch

	if branchName == "" {
		return fmt.Errorf("cannot finish worktree on detached HEAD: %s", worktreePath)
	}

	fmt.Printf("\nSelected worktree '%s' on branch '%s'\n", filepath.Base(worktreePath), branchName)

	// 3. Sprawdź czy port w .env jest używany
	envFile := filepath.Join(worktreePath, ".env")
	if port, err := readEnvPort(envFile); err == nil {
		if isPortInUse(port) {
			if force {
				fmt.Fprintf(os.Stderr, "Warning: Server port %d is currently active! Continuing due to --force.\n", port)
			} else {
				return fmt.Errorf("server on port %d is still running for worktree '%s'. Please close the server before finishing, or run with --force", port, branchName)
			}
		}
	}

	// 4. Następny krok zostanie uruchomiony na tamtym branchu: git rebase main
	// W razie problemów cofnij skutki rebase (git rebase --abort) i zakończ działanie
	fmt.Printf("Running 'git rebase main' in %s...\n", worktreePath)
	if out, err := cmdCombinedOutputInDir(worktreePath, "git", "rebase", "main"); err != nil {
		if strings.Contains(out, "cannot rebase: You have unstaged changes") || strings.Contains(out, "unstaged changes") {
			fmt.Fprintln(os.Stderr, "error: cannot rebase: You have unstaged changes.")
			fmt.Fprintln(os.Stderr, "error: Please commit or stash them.")
			return silentError{}
		}
		if out != "" {
			fmt.Fprintln(os.Stderr, out)
		}
		fmt.Fprintf(os.Stderr, "Error during 'git rebase main' in %s. Aborting rebase to revert state...\n", worktreePath)
		_ = runCmdInDir(worktreePath, "git", "rebase", "--abort")
		return fmt.Errorf("rebase failed in worktree '%s': %w", branchName, err)
	}
	fmt.Println("Successfully rebased worktree branch on main.")

	// 5. Wróć na główną gałąź i zrób rebase z tym branchem
	mainBranch, err := cmdOutput("git", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return fmt.Errorf("failed getting current main branch: %w", err)
	}
	fmt.Printf("Rebasing main branch '%s' with '%s'...\n", mainBranch, branchName)
	if out, err := cmdCombinedOutput("git", "rebase", branchName); err != nil {
		if strings.Contains(out, "cannot rebase: You have unstaged changes") || strings.Contains(out, "unstaged changes") {
			fmt.Fprintln(os.Stderr, "error: cannot rebase: You have unstaged changes.")
			fmt.Fprintln(os.Stderr, "error: Please commit or stash them.")
			return silentError{}
		}
		if out != "" {
			fmt.Fprintln(os.Stderr, out)
		}
		fmt.Fprintf(os.Stderr, "Error rebasing %s with %s. Aborting rebase on main...\n", mainBranch, branchName)
		_ = runCmd("git", "rebase", "--abort")
		return fmt.Errorf("rebase on main branch failed: %w", err)
	}
	fmt.Printf("Main branch '%s' successfully rebased with '%s'.\n", mainBranch, branchName)

	// 6. Następnie usuń worktree i pliki
	fmt.Printf("Removing worktree at %s...\n", worktreePath)
	if err := runCmd("git", "worktree", "remove", "--force", worktreePath); err != nil {
		_ = os.RemoveAll(worktreePath)
	}
	_ = runCmd("git", "worktree", "prune")

	// Usuń gałąź branchName jeśli jest już zintegrowana
	if branchName != "main" && branchName != "master" {
		_ = runCmd("git", "branch", "-d", branchName)
	}

	fmt.Printf("Worktree '%s' and branch '%s' finished and cleaned up successfully!\n", filepath.Base(worktreePath), branchName)
	return nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run ./scripts/worktree.go {create <name>|clear <name> [--delete-branch] [--force]|update-maintree <name>|update-branch|finish [--force]}")
		os.Exit(1)
	}

	action := os.Args[1]
	args := os.Args[2:]

	var err error
	switch action {
	case "create":
		err = cmdCreate(args)
	case "clear", "clean":
		err = cmdClear(args)
	case "update-maintree":
		err = cmdUpdateMaintree(args)
	case "update-branch":
		err = cmdUpdateBranch(args)
	case "finish":
		err = cmdFinish(args)
	default:
		err = fmt.Errorf("unknown action: %s", action)
	}

	if err != nil {
		if _, ok := err.(silentError); ok {
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
