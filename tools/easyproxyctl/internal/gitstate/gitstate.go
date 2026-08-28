package gitstate

import (
	"bufio"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type State struct {
	RootCommit string
	Submodules map[string]string
}

func Collect(repoRoot string) (State, error) {
	rootCommit, err := gitOutput(repoRoot, "rev-parse", "HEAD")
	if err != nil {
		return State{}, err
	}
	status, err := gitOutput(repoRoot, "submodule", "status", "--recursive")
	if err != nil {
		return State{}, err
	}
	submodules, err := parseSubmoduleStatus(status)
	if err != nil {
		return State{}, err
	}
	return State{RootCommit: rootCommit, Submodules: submodules}, nil
}

func gitOutput(repoRoot string, args ...string) (string, error) {
	arguments := append([]string{"-C", repoRoot}, args...)
	output, err := exec.Command("git", arguments...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func parseSubmoduleStatus(status string) (map[string]string, error) {
	result := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(status))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, fmt.Errorf("malformed git submodule status line %q", line)
		}
		commit := strings.TrimLeft(fields[0], "-+U")
		if commit == "" || fields[1] == "" {
			return nil, fmt.Errorf("malformed git submodule status line %q", line)
		}
		if _, exists := result[fields[1]]; exists {
			return nil, fmt.Errorf("duplicate submodule path %q", fields[1])
		}
		result[fields[1]] = commit
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.New("scan git submodule status: " + err.Error())
	}
	return result, nil
}
