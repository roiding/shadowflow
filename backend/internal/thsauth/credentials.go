package thsauth

import (
	"context"
	"strings"
)

// FileCredentials returns a CredentialSource backed by the hardened credentials
// file (private-permission JSON on disk).
func FileCredentials(path string) CredentialSource {
	return fileCredentialSource{path: path}
}

// EnvCredentials returns a CredentialSource backed by process-provided values,
// so deployments can supply credentials through compose environment variables
// without mounting a file.
func EnvCredentials(username, password string) CredentialSource {
	return envCredentialSource{username: username, password: password}
}

type fileCredentialSource struct{ path string }

func (source fileCredentialSource) Credentials(ctx context.Context) (Credentials, error) {
	return LoadCredentials(source.path)
}

type envCredentialSource struct {
	username string
	password string
}

func (source envCredentialSource) Credentials(ctx context.Context) (Credentials, error) {
	credentials := Credentials{Username: strings.TrimSpace(source.username), Password: source.password}
	if err := validateCredentials(credentials); err != nil {
		return Credentials{}, err
	}
	return credentials, nil
}
