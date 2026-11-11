package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// There are no flags for the key: a flag value lands in shell history and
// process listings. Scripts use these environment variables.
const (
	keyIDEnv     = "KUBESENSE_ACCESS_KEY_ID"
	keySecretEnv = "KUBESENSE_ACCESS_KEY_SECRET"
)

// askAccessKey reads the access key ID and secret from the environment, else
// asks for them on the terminal; the secret is not echoed.
func askAccessKey() (id, secret string, err error) {
	id = strings.TrimSpace(os.Getenv(keyIDEnv))
	secret = strings.TrimSpace(os.Getenv(keySecretEnv))
	if id != "" && secret != "" {
		return id, secret, nil
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", "", errors.New("no terminal to ask on: set " + keyIDEnv + " and " + keySecretEnv)
	}
	if id == "" {
		fmt.Fprint(os.Stderr, "Access key ID: ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return "", "", err
		}
		id = strings.TrimSpace(line)
	}
	if secret == "" {
		fmt.Fprint(os.Stderr, "Access key secret (not shown): ")
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", "", err
		}
		secret = strings.TrimSpace(string(b))
	}
	if id == "" || secret == "" {
		return "", "", errors.New("access key ID and secret are both required")
	}
	return id, secret, nil
}
